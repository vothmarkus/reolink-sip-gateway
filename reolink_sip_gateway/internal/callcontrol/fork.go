package callcontrol

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrNoDialLegs = errors.New("no SIP call legs are available")

const loserHangupTimeout = 5 * time.Second

// Dialog is the minimum established-call contract required by the forking
// controller. The SIP implementation ACKs every successful INVITE before a
// Dialog is returned; Hangup therefore maps to the mandatory BYE cleanup for a
// losing 2xx response.
type Dialog interface {
	Hangup(context.Context) error
}

// DialLeg describes one independently routed INVITE. Several legs may share a
// SIP account as long as that account allows the corresponding number of
// concurrent dialogs.
type DialLeg struct {
	ID          string
	Destination string
	Dial        func(context.Context) (Dialog, error)
}

// ForkWinner is the first successfully established leg. LosersDone closes
// after every other leg has either completed its CANCEL path or, if it crossed
// the winner decision with a 2xx response, has been hung up with BYE.
type ForkWinner struct {
	Leg        DialLeg
	Dialog     Dialog
	LosersDone <-chan struct{}
}

type dialResult struct {
	leg    DialLeg
	dialog Dialog
	err    error
}

// DialFirst races all legs and atomically selects the first established
// dialog. Canceling the shared dialing context makes still-ringing SIP legs
// send CANCEL. A concurrent/late success is drained in the background and
// receives BYE, so media can start immediately on the winner without waiting
// for every losing SIP transaction to finish.
func DialFirst(ctx context.Context, legs []DialLeg) (ForkWinner, error) {
	if err := ctx.Err(); err != nil {
		return ForkWinner{LosersDone: closedCleanupSignal()}, err
	}
	if len(legs) == 0 {
		return ForkWinner{LosersDone: closedCleanupSignal()}, ErrNoDialLegs
	}
	for _, leg := range legs {
		if leg.Dial == nil {
			return ForkWinner{LosersDone: closedCleanupSignal()}, fmt.Errorf("SIP call leg %q has no dial function", leg.ID)
		}
	}

	dialCtx, cancel := context.WithCancel(ctx)
	results := make(chan dialResult, len(legs))
	for _, leg := range legs {
		leg := leg
		go func() {
			dialog, err := leg.Dial(dialCtx)
			results <- dialResult{leg: leg, dialog: dialog, err: err}
		}()
	}

	var failures []error
	remaining := len(legs)
	for remaining > 0 {
		select {
		case result := <-results:
			remaining--
			if result.err != nil {
				failures = append(failures, fmt.Errorf("%s (%s): %w", result.leg.ID, result.leg.Destination, result.err))
				continue
			}
			if result.dialog == nil {
				failures = append(failures, fmt.Errorf("%s (%s): dial returned no dialog", result.leg.ID, result.leg.Destination))
				continue
			}

			cancel()
			losersDone := make(chan struct{})
			go drainLosingDialogs(results, remaining, losersDone)
			return ForkWinner{Leg: result.leg, Dialog: result.dialog, LosersDone: losersDone}, nil
		case <-ctx.Done():
			cancel()
			losersDone := make(chan struct{})
			go drainLosingDialogs(results, remaining, losersDone)
			return ForkWinner{LosersDone: losersDone}, ctx.Err()
		}
	}

	cancel()
	return ForkWinner{LosersDone: closedCleanupSignal()}, errors.Join(failures...)
}

func drainLosingDialogs(results <-chan dialResult, remaining int, done chan<- struct{}) {
	defer close(done)
	var hangups sync.WaitGroup
	for range remaining {
		result := <-results
		if result.err != nil || result.dialog == nil {
			continue
		}
		hangups.Add(1)
		go func(dialog Dialog) {
			defer hangups.Done()
			ctx, cancel := context.WithTimeout(context.Background(), loserHangupTimeout)
			_ = dialog.Hangup(ctx)
			cancel()
		}(result.dialog)
	}
	hangups.Wait()
}

func closedCleanupSignal() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
