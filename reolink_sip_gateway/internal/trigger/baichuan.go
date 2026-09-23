package trigger

import (
	"context"
	"fmt"
	"log/slog"
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
	Messages            uint64     `json:"messages"`
	ChannelEvents       uint64     `json:"channel_events"`
	VisitorStates       uint64     `json:"visitor_states"`
	Emitted             uint64     `json:"emitted"`
	InvalidMessages     uint64     `json:"invalid_messages"`
	InitialActiveStates uint64     `json:"initial_active_states"`
	LastChannel         int        `json:"last_channel"`
	LastMessageAt       *time.Time `json:"last_message_at,omitempty"`
}

func (l *Baichuan) Run(ctx context.Context, out chan<- Event) error {
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
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
		if l.Logger != nil {
			l.Logger.Warn("Reolink event connection unavailable; reconnecting", "error", err, "retry_in", backoff)
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
	client, err := baichuan.Dial(connectCtx, l.Config)
	if err != nil {
		return err
	}
	defer client.Close()
	// Some models publish a snapshot while subscribing; others send nothing
	// until the first press. Only the short subscription window is a baseline.
	events, unsubscribe, err := client.SubscribeEvents(connectCtx)
	if err != nil {
		return err
	}
	defer unsubscribe()
	edge := visitorEdge{snapshotUntil: time.Now().Add(2 * time.Second)}
	if l.OnConnection != nil {
		l.OnConnection(true)
	}
	if l.Logger != nil {
		l.Logger.Info("Reolink visitor subscription ready", "channel", l.Channel+1)
	}
	renew := time.NewTicker(30 * time.Second)
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-client.Done():
			return client.Err()
		case <-renew.C:
			checkCtx, done := context.WithTimeout(ctx, 10*time.Second)
			err := client.RenewEvents(checkCtx)
			done()
			if err != nil {
				return fmt.Errorf("renew visitor subscription: %w", err)
			}
		case message := <-events:
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
			if l.OnDiagnostics != nil {
				l.OnDiagnostics(l.diagnostics)
			}
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
