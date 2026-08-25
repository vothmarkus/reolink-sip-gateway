package callcontrol

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDialog struct {
	hangups atomic.Int32
}

func (d *fakeDialog) Hangup(context.Context) error {
	d.hangups.Add(1)
	return nil
}

func TestDialFirstCancelsRingingLegAndHangsUpLateSuccess(t *testing.T) {
	winnerDialog := &fakeDialog{}
	lateDialog := &fakeDialog{}
	ringingCanceled := make(chan struct{})
	legs := []DialLeg{
		{
			ID: "door", Destination: "**610",
			Dial: func(context.Context) (Dialog, error) {
				time.Sleep(20 * time.Millisecond)
				return winnerDialog, nil
			},
		},
		{
			ID: "mobile-1", Destination: "0163",
			Dial: func(ctx context.Context) (Dialog, error) {
				<-ctx.Done()
				close(ringingCanceled)
				return nil, ctx.Err()
			},
		},
		{
			ID: "mobile-2", Destination: "0176",
			Dial: func(context.Context) (Dialog, error) {
				// Model a 200 OK that crossed cancellation on the wire.
				time.Sleep(40 * time.Millisecond)
				return lateDialog, nil
			},
		},
	}

	winner, err := DialFirst(context.Background(), legs)
	if err != nil {
		t.Fatal(err)
	}
	if winner.Leg.ID != "door" || winner.Dialog != winnerDialog {
		t.Fatalf("unexpected winner: %#v", winner.Leg)
	}
	select {
	case <-ringingCanceled:
	case <-time.After(time.Second):
		t.Fatal("ringing leg was not canceled")
	}
	select {
	case <-winner.LosersDone:
	case <-time.After(time.Second):
		t.Fatal("loser cleanup did not finish")
	}
	if winnerDialog.hangups.Load() != 0 {
		t.Fatal("winner was hung up by loser cleanup")
	}
	if lateDialog.hangups.Load() != 1 {
		t.Fatalf("late successful loser hangups=%d want=1", lateDialog.hangups.Load())
	}
}

func TestDialFirstReturnsCombinedFailures(t *testing.T) {
	legs := []DialLeg{
		{ID: "door", Destination: "**610", Dial: func(context.Context) (Dialog, error) { return nil, errors.New("486 Busy Here") }},
		{ID: "mobile-1", Destination: "0163", Dial: func(context.Context) (Dialog, error) { return nil, errors.New("503 unavailable") }},
	}
	_, err := DialFirst(context.Background(), legs)
	if err == nil || !strings.Contains(err.Error(), "door (**610)") || !strings.Contains(err.Error(), "mobile-1 (0163)") {
		t.Fatalf("unexpected combined error: %v", err)
	}
}

func TestDialFirstRejectsEmptyOrCanceledInput(t *testing.T) {
	if _, err := DialFirst(context.Background(), nil); !errors.Is(err, ErrNoDialLegs) {
		t.Fatalf("empty legs error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DialFirst(ctx, []DialLeg{{ID: "door", Dial: func(context.Context) (Dialog, error) { return &fakeDialog{}, nil }}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error=%v", err)
	}
}
