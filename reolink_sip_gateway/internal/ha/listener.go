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
	Routes       []RouteSubscription
	PollInterval time.Duration
	Client       *http.Client
	Logger       *slog.Logger
	OnConnection func(bool)

	previous    map[string]string
	initialized map[string]bool
	connectedAt time.Time
}

type RouteSubscription struct {
	RouteID  string
	EntityID string
}

type Trigger struct {
	RouteID  string
	EntityID string
}

type routeStateResult struct {
	route RouteSubscription
	state string
	err   error
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
				EntityID string `json:"entity_id"`
				State    string `json:"state"`
			} `json:"to_state"`
		} `json:"trigger"`
	} `json:"variables"`
}

func (l *Listener) Run(ctx context.Context, trigger chan<- Trigger) error {
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
	if len(l.Routes) == 0 {
		return errors.New("at least one Home Assistant call route is required")
	}
	if l.previous == nil {
		l.previous = make(map[string]string, len(l.Routes))
	}
	if l.initialized == nil {
		l.initialized = make(map[string]bool, len(l.Routes))
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

func (l *Listener) runWebSocket(ctx context.Context, trigger chan<- Trigger) error {
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

	entityIDs := make([]string, 0, len(l.Routes))
	for _, route := range l.Routes {
		entityIDs = append(entityIDs, route.EntityID)
	}
	subscribe := map[string]any{
		"id":   1,
		"type": "subscribe_trigger",
		"trigger": map[string]any{
			"platform":  "state",
			"entity_id": entityIDs,
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
		l.Logger.Info("Home Assistant call-route trigger subscription active", "route_count", len(l.Routes))
	}
	// Seed the REST-fallback edge detector without making REST availability a
	// prerequisite for WebSocket operation. Because the subscription is already
	// active, any concurrent off->on transition is queued on the WebSocket.
	for _, result := range l.fetchRouteStates(ctx) {
		if result.err == nil {
			l.previous[result.route.EntityID] = strings.ToLower(strings.TrimSpace(result.state))
			l.initialized[result.route.EntityID] = true
		} else if l.Logger != nil {
			// WebSocket operation is unaffected, but surfacing this at warning level
			// makes a mistyped/removed entity visible instead of silently leaving the
			// REST fallback without an initial edge state.
			l.Logger.Warn("could not verify current HA route sensor state for fallback", "route", result.route.RouteID, "entity", result.route.EntityID, "error", result.err)
		}
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
			entityID := strings.TrimSpace(ev.Variables.Trigger.ToState.EntityID)
			route, ok := l.routeForEntity(entityID)
			if !ok {
				if l.Logger != nil {
					l.Logger.Warn("Home Assistant trigger referenced an unknown route entity", "entity", entityID)
				}
				continue
			}
			l.previous[entityID] = "on"
			l.initialized[entityID] = true
			l.emitTrigger(trigger, route)
		}
	}
}

func (l *Listener) pollFallback(ctx context.Context, trigger chan<- Trigger, duration time.Duration) error {
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(l.PollInterval)
	defer ticker.Stop()

	// Poll immediately, then at the configured interval until reconnect time.
	for {
		allConnected := true
		for _, result := range l.fetchRouteStates(ctx) {
			if result.err == nil {
				l.acceptState(result.route, result.state, trigger)
				continue
			}
			allConnected = false
			if l.Logger != nil {
				l.Logger.Warn("Home Assistant fallback state read failed", "route", result.route.RouteID, "entity", result.route.EntityID, "error", result.err)
			}
		}
		if l.OnConnection != nil {
			l.OnConnection(allConnected)
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

func (l *Listener) acceptState(route RouteSubscription, state string, trigger chan<- Trigger) {
	state = strings.ToLower(strings.TrimSpace(state))
	if !l.initialized[route.EntityID] {
		l.previous[route.EntityID] = state
		l.initialized[route.EntityID] = true
		return
	}
	if l.previous[route.EntityID] != "on" && state == "on" {
		l.emitTrigger(trigger, route)
	}
	l.previous[route.EntityID] = state
}

func (l *Listener) emitTrigger(trigger chan<- Trigger, route RouteSubscription) {
	select {
	case trigger <- Trigger{RouteID: route.RouteID, EntityID: route.EntityID}:
	default:
		if l.Logger != nil {
			l.Logger.Warn("call-route event dropped because trigger queue is full", "route", route.RouteID)
		}
	}
}

func (l *Listener) routeForEntity(entityID string) (RouteSubscription, bool) {
	for _, route := range l.Routes {
		if route.EntityID == entityID {
			return route, true
		}
	}
	return RouteSubscription{}, false
}

func (l *Listener) fetchRouteStates(ctx context.Context) []routeStateResult {
	results := make(chan routeStateResult, len(l.Routes))
	for _, route := range l.Routes {
		route := route
		go func() {
			state, err := l.fetchState(ctx, route.EntityID)
			results <- routeStateResult{route: route, state: state, err: err}
		}()
	}
	collected := make([]routeStateResult, 0, len(l.Routes))
	for range l.Routes {
		collected = append(collected, <-results)
	}
	return collected
}

func (l *Listener) fetchState(ctx context.Context, entityID string) (string, error) {
	endpoint := strings.TrimRight(l.RESTBaseURL, "/") + "/states/" + url.PathEscape(entityID)
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
