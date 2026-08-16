package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Listener struct {
	WSURL        string
	RESTBaseURL  string
	Token        string
	EntityID     string
	PollInterval time.Duration
	Client       *http.Client
	Logger       *slog.Logger
	OnConnection func(bool)

	previous    string
	initialized bool
	connectedAt time.Time
}

type wsEnvelope struct {
	ID      int             `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Event   json.RawMessage `json:"event"`
}

type stateResponse struct {
	State string `json:"state"`
}

type triggerEvent struct {
	Variables struct {
		Trigger struct {
			ToState *struct {
				State string `json:"state"`
			} `json:"to_state"`
		} `json:"trigger"`
	} `json:"variables"`
}

func (l *Listener) Run(ctx context.Context, trigger chan<- struct{}) error {
	if l.WSURL == "" {
		l.WSURL = "ws://supervisor/core/websocket"
	}
	if l.RESTBaseURL == "" {
		l.RESTBaseURL = "http://supervisor/core/api"
	}
	if l.Client == nil {
		l.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if l.PollInterval <= 0 {
		l.PollInterval = time.Second
	}

	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := l.runWebSocket(ctx, trigger)
		if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil) {
			return ctx.Err()
		}
		if !l.connectedAt.IsZero() && time.Since(l.connectedAt) >= 10*time.Second {
			backoff = time.Second
		}
		if l.OnConnection != nil {
			l.OnConnection(false)
		}
		if l.Logger != nil {
			l.Logger.Warn("Home Assistant websocket disconnected; using REST fallback", "error", err, "retry_in", backoff)
		}
		if err := l.pollFallback(ctx, trigger, backoff); err != nil && !errors.Is(err, context.Canceled) {
			if l.Logger != nil {
				l.Logger.Warn("Home Assistant REST fallback failed", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if backoff < 15*time.Second {
			backoff *= 2
			if backoff > 15*time.Second {
				backoff = 15 * time.Second
			}
		}
	}
}

func (l *Listener) runWebSocket(ctx context.Context, trigger chan<- struct{}) error {
	conn, err := dialWebSocket(ctx, l.WSURL)
	if err != nil {
		return err
	}
	defer conn.Close()

	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	raw, err := conn.ReadMessage(readCtx)
	if err != nil {
		return fmt.Errorf("read HA websocket auth challenge: %w", err)
	}
	var authReq wsEnvelope
	if err := json.Unmarshal(raw, &authReq); err != nil || authReq.Type != "auth_required" {
		return fmt.Errorf("unexpected HA websocket auth challenge: %s", strings.TrimSpace(string(raw)))
	}
	auth, _ := json.Marshal(map[string]any{"type": "auth", "access_token": l.Token})
	if err := conn.WriteJSON(auth); err != nil {
		return fmt.Errorf("send HA websocket auth: %w", err)
	}
	raw, err = conn.ReadMessage(readCtx)
	if err != nil {
		return fmt.Errorf("read HA websocket auth result: %w", err)
	}
	var authRes wsEnvelope
	if err := json.Unmarshal(raw, &authRes); err != nil {
		return fmt.Errorf("decode HA websocket auth result: %w", err)
	}
	if authRes.Type != "auth_ok" {
		return fmt.Errorf("Home Assistant websocket authentication failed: %s", authRes.Message)
	}

	subscribe := map[string]any{
		"id":   1,
		"type": "subscribe_trigger",
		"trigger": map[string]any{
			"platform":  "state",
			"entity_id": l.EntityID,
			"from":      "off",
			"to":        "on",
		},
	}
	payload, _ := json.Marshal(subscribe)
	if err := conn.WriteJSON(payload); err != nil {
		return fmt.Errorf("subscribe HA trigger: %w", err)
	}
	raw, err = conn.ReadMessage(readCtx)
	if err != nil {
		return fmt.Errorf("read HA subscription result: %w", err)
	}
	var subRes wsEnvelope
	if err := json.Unmarshal(raw, &subRes); err != nil {
		return fmt.Errorf("decode HA subscription result: %w", err)
	}
	if subRes.Type != "result" || subRes.ID != 1 || !subRes.Success {
		return fmt.Errorf("Home Assistant rejected visitor trigger subscription: %s", subRes.Message)
	}
	if l.OnConnection != nil {
		l.OnConnection(true)
	}
	l.connectedAt = time.Now()
	if l.Logger != nil {
		l.Logger.Info("Home Assistant visitor trigger subscription active", "entity", l.EntityID)
	}
	// Seed the REST-fallback edge detector without making REST availability a
	// prerequisite for WebSocket operation. Because the subscription is already
	// active, any concurrent off->on transition is queued on the WebSocket.
	if state, err := l.fetchState(ctx); err == nil {
		l.previous = strings.ToLower(strings.TrimSpace(state))
		l.initialized = true
	} else if l.Logger != nil {
		// WebSocket operation is unaffected, but surfacing this at warning level
		// makes a mistyped/removed entity visible instead of silently leaving the
		// REST fallback without an initial edge state.
		l.Logger.Warn("could not verify current HA visitor sensor state for fallback", "entity", l.EntityID, "error", err)
	}

	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := conn.Ping(); err != nil {
					_ = conn.conn.Close() // Wake the reader; reconnect happens in Run.
					return
				}
			}
		}
	}()

	for {
		raw, err := conn.ReadMessage(readCtx)
		if err != nil {
			return fmt.Errorf("read HA websocket event: %w", err)
		}
		var msg wsEnvelope
		if err := json.Unmarshal(raw, &msg); err != nil {
			if l.Logger != nil {
				l.Logger.Warn("invalid Home Assistant websocket message", "error", err)
			}
			continue
		}
		if msg.Type != "event" || msg.ID != 1 {
			continue
		}
		var ev triggerEvent
		if err := json.Unmarshal(msg.Event, &ev); err != nil || ev.Variables.Trigger.ToState == nil {
			if l.Logger != nil {
				l.Logger.Warn("invalid Home Assistant trigger event", "error", err)
			}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(ev.Variables.Trigger.ToState.State), "on") {
			l.previous = "on"
			l.initialized = true
			l.emitTrigger(trigger)
		}
	}
}

func (l *Listener) pollFallback(ctx context.Context, trigger chan<- struct{}, duration time.Duration) error {
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(l.PollInterval)
	defer ticker.Stop()

	// Poll immediately, then at the configured interval until reconnect time.
	for {
		state, err := l.fetchState(ctx)
		if err == nil {
			if l.OnConnection != nil {
				l.OnConnection(true)
			}
			l.acceptState(state, trigger)
		} else {
			if l.OnConnection != nil {
				l.OnConnection(false)
			}
			if l.Logger != nil {
				l.Logger.Warn("Home Assistant fallback state read failed", "entity", l.EntityID, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-ticker.C:
		}
	}
}

func (l *Listener) acceptState(state string, trigger chan<- struct{}) {
	state = strings.ToLower(strings.TrimSpace(state))
	if !l.initialized {
		l.previous = state
		l.initialized = true
		return
	}
	if l.previous != "on" && state == "on" {
		l.emitTrigger(trigger)
	}
	l.previous = state
}

func (l *Listener) emitTrigger(trigger chan<- struct{}) {
	select {
	case trigger <- struct{}{}:
	default:
		if l.Logger != nil {
			l.Logger.Warn("visitor event dropped because trigger queue is full")
		}
	}
}

func (l *Listener) fetchState(ctx context.Context) (string, error) {
	endpoint := strings.TrimRight(l.RESTBaseURL, "/") + "/states/" + url.PathEscape(l.EntityID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+l.Token)
	req.Header.Set("Accept", "application/json")
	res, err := l.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Home Assistant API returned %s", res.Status)
	}
	var body stateResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.State, nil
}
