package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
)

type Baichuan struct {
	Config       baichuan.Config
	Channel      int
	RouteID      string
	Logger       *slog.Logger
	OnConnection func(bool)
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
	events, unsubscribe, err := client.SubscribeEvents(connectCtx)
	if err != nil {
		return err
	}
	defer unsubscribe()
	if l.OnConnection != nil {
		l.OnConnection(true)
	}
	if l.Logger != nil {
		l.Logger.Info("Reolink visitor subscription ready", "channel", l.Channel+1)
	}
	renew := time.NewTicker(30 * time.Second)
	defer renew.Stop()
	var edge visitorEdge
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
			alarms, err := baichuan.ParseAlarmEvents(message.XML)
			if err != nil {
				if l.Logger != nil {
					l.Logger.Warn("invalid Reolink alarm event ignored", "error", err)
				}
				continue
			}
			for _, alarm := range alarms {
				if alarm.Channel != l.Channel || !edge.update(alarm.Visitor) {
					continue
				}
				select {
				case out <- Event{RouteID: l.RouteID, EntityID: fmt.Sprintf("reolink.channel.%d.visitor", l.Channel+1)}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

type visitorEdge struct{ initialized, previous bool }

func (s *visitorEdge) update(visitor bool) bool {
	// The first state after each subscription is a snapshot, never a new press.
	rising := s.initialized && !s.previous && visitor
	s.initialized, s.previous = true, visitor
	return rising
}
