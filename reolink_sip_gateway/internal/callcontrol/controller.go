package callcontrol

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrBusy         = errors.New("another call is active")
	ErrNoActiveCall = errors.New("no call is active")
)

// Controller owns the single-call invariant above the SIP and media layers.
// Every call source (visitor event, incoming INVITE or integration test-call
// request) must enter through Start. CancelActive is consequently able to stop
// the complete call context without reaching into either protocol package.
type Controller struct {
	mu         sync.Mutex
	active     bool
	generation uint64
	cancel     context.CancelFunc
}

// Start reserves the one available call slot and runs fn asynchronously. The
// slot remains occupied until fn has completed all SIP and media cleanup.
func (c *Controller) Start(parent context.Context, fn func(context.Context)) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("call function is nil")
	}

	c.mu.Lock()
	if c.active {
		c.mu.Unlock()
		return ErrBusy
	}
	callCtx, cancel := context.WithCancel(parent)
	c.active = true
	c.generation++
	generation := c.generation
	c.cancel = cancel
	c.mu.Unlock()

	go func() {
		defer cancel()
		defer func() {
			c.mu.Lock()
			if c.generation == generation {
				c.active = false
				c.cancel = nil
			}
			c.mu.Unlock()
		}()
		fn(callCtx)
	}()
	return nil
}

// CancelActive invokes beforeCancel while the call slot is still reserved and
// then cancels the call context. This ordering lets callers publish an
// "ending" state without racing the call goroutine's final "idle" update.
func (c *Controller) CancelActive(beforeCancel func()) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.cancel == nil {
		return ErrNoActiveCall
	}
	if beforeCancel != nil {
		beforeCancel()
	}
	c.cancel()
	return nil
}

func (c *Controller) Active() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}
