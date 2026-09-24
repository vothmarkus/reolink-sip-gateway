package trigger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
)

type Baichuan struct {
	Config        baichuan.Config
	Channel       int
	RouteID       string
	Logger        *slog.Logger
	OnConnection  func(bool)
	OnDiagnostics func(Diagnostics)
	diagnostics   Diagnostics
}

// Diagnostics contains no raw camera messages or credentials. Counts survive
// subscription reconnects and reset when the gateway is restarted.
type Diagnostics struct {
	Stage                 string     `json:"stage,omitempty"`
	ConnectionAttempts    uint64     `json:"connection_attempts"`
	SuccessfulConnections uint64     `json:"successful_connections"`
	LastAttemptAt         *time.Time `json:"last_attempt_at,omitempty"`
	LastConnectedAt       *time.Time `json:"last_connected_at,omitempty"`
	LastError             string     `json:"last_error,omitempty"`
	LastErrorStage        string     `json:"last_error_stage,omitempty"`
	LastErrorAt           *time.Time `json:"last_error_at,omitempty"`
	NextRetryAt           *time.Time `json:"next_retry_at,omitempty"`
	Messages              uint64     `json:"messages"`
	ChannelEvents         uint64     `json:"channel_events"`
	VisitorStates         uint64     `json:"visitor_states"`
	Emitted               uint64     `json:"emitted"`
	InvalidMessages       uint64     `json:"invalid_messages"`
	InitialActiveStates   uint64     `json:"initial_active_states"`
	LastChannel           int        `json:"last_channel"`
	LastMessageAt         *time.Time `json:"last_message_at,omitempty"`
}

func (l *Baichuan) Run(ctx context.Context, out chan<- Event) error {
	defer l.setStage("stopped")
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		l.diagnostics.ConnectionAttempts++
		l.diagnostics.LastAttemptAt = &started
		l.diagnostics.NextRetryAt = nil
		l.setStage("connecting")
		err := l.session(ctx, out)
		if l.OnConnection != nil {
			l.OnConnection(false)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		failedAt := time.Now()
		retryAt := failedAt.Add(backoff)
		l.diagnostics.LastError = publicConnectionError(err, l.Config)
		l.diagnostics.LastErrorStage = l.diagnostics.Stage
		l.diagnostics.LastErrorAt = &failedAt
		l.diagnostics.NextRetryAt = &retryAt
		l.setStage("retry_wait")
		if l.Logger != nil {
			l.Logger.Warn("Reolink event connection unavailable; reconnecting", "error", l.diagnostics.LastError, "stage", l.diagnostics.LastErrorStage, "retry_in", backoff)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, 30*time.Second)
	}
	return ctx.Err()
}

func (l *Baichuan) session(ctx context.Context, out chan<- Event) error {
	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	eventConfig := l.Config
	eventConfig.ControlChannel = 250 // Host-level login/keepalive, as in reolink_aio.
	client, err := baichuan.Dial(connectCtx, eventConfig)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.LoginWithProgress(connectCtx, l.setStage); err != nil {
		return err
	}
	l.setStage("subscribing")
	// Some models publish a snapshot while subscribing; others send nothing
	// until the first press. Only the short subscription window is a baseline.
	events, unsubscribe, err := client.SubscribeEvents(connectCtx)
	if err != nil {
		return err
	}
	defer unsubscribe()
	connectedAt := time.Now()
	l.diagnostics.SuccessfulConnections++
	l.diagnostics.LastConnectedAt = &connectedAt
	l.diagnostics.LastError, l.diagnostics.LastErrorStage = "", ""
	l.diagnostics.LastErrorAt, l.diagnostics.NextRetryAt = nil, nil
	l.setStage("listening")
	edge := visitorEdge{snapshotUntil: connectedAt.Add(2 * time.Second)}
	if l.OnConnection != nil {
		l.OnConnection(true)
	}
	if l.Logger != nil {
		l.Logger.Info("Reolink visitor subscription ready", "channel", l.Channel+1)
	}
	renew := time.NewTicker(30 * time.Second)
	defer renew.Stop()
	receivedEvents := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-client.Done():
			return client.Err()
		case <-renew.C:
			checkCtx, done := context.WithTimeout(ctx, 10*time.Second)
			l.setStage("keepalive")
			var err error
			if receivedEvents {
				err = client.CheckEventConnection(checkCtx)
			} else {
				err = client.RenewEvents(checkCtx)
			}
			done()
			if err != nil {
				return fmt.Errorf("check visitor connection: %w", err)
			}
			l.setStage("listening")
		case message := <-events:
			receivedEvents = true
			now := time.Now()
			l.diagnostics.Messages++
			l.diagnostics.LastMessageAt = &now
			alarms, err := baichuan.ParseAlarmEvents(message.XML)
			if err != nil {
				l.diagnostics.InvalidMessages++
				if l.Logger != nil {
					l.Logger.Warn("invalid Reolink alarm event ignored", "error", err)
				}
			}
			for _, alarm := range alarms {
				l.diagnostics.LastChannel = alarm.Channel
				if alarm.Channel != l.Channel {
					continue
				}
				l.diagnostics.ChannelEvents++
				if alarm.Visitor {
					l.diagnostics.VisitorStates++
				}
				if !edge.initialized && alarm.Visitor && now.Before(edge.snapshotUntil) {
					l.diagnostics.InitialActiveStates++
				}
				if !edge.update(alarm.Visitor, now) {
					continue
				}
				select {
				case out <- Event{RouteID: l.RouteID, EntityID: fmt.Sprintf("reolink.channel.%d.visitor", l.Channel+1)}:
					l.diagnostics.Emitted++
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			l.publishDiagnostics()
		}
	}
}

type visitorEdge struct {
	initialized, previous bool
	snapshotUntil         time.Time
}

func (s *visitorEdge) update(visitor bool, now time.Time) bool {
	// Do not discard a real first press minutes after subscribing just because
	// this firmware never sent an initial idle snapshot. Once an idle state has
	// arrived, its rising edge is valid even inside the startup window.
	rising := !s.previous && visitor && (s.initialized || !now.Before(s.snapshotUntil))
	s.initialized, s.previous = true, visitor
	return rising
}

func (l *Baichuan) publishDiagnostics() {
	if l.OnDiagnostics != nil {
		l.OnDiagnostics(l.diagnostics)
	}
}
func (l *Baichuan) setStage(stage string) {
	l.diagnostics.Stage = stage
	l.publishDiagnostics()
}

func publicConnectionError(err error, cfg baichuan.Config) string {
	if err == nil {
		return "camera connection ended"
	}
	var status *baichuan.StatusError
	if errors.As(err, &status) {
		return fmt.Sprintf("Baichuan command %d rejected by camera (status %d)", status.MsgID, status.Code)
	}
	// Protocol errors contain only metadata; Login deliberately omits raw nonce
	// XML and authentication material. Redact configured credentials as well.
	message := err.Error()
	for _, secret := range []string{cfg.Password, cfg.Username} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
