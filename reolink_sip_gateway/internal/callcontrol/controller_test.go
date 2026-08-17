package callcontrol

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestControllerSerializesAndCancelsCalls(t *testing.T) {
	var controller Controller
	started := make(chan struct{})
	stopped := make(chan struct{})
	if err := controller.Start(context.Background(), func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	}); err != nil {
		t.Fatalf("start call: %v", err)
	}
	<-started
	if !controller.Active() {
		t.Fatal("controller should report an active call")
	}
	if err := controller.Start(context.Background(), func(context.Context) {}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second call error = %v, want ErrBusy", err)
	}

	endingPublished := false
	if err := controller.CancelActive(func() { endingPublished = true }); err != nil {
		t.Fatalf("cancel active call: %v", err)
	}
	if !endingPublished {
		t.Fatal("beforeCancel was not invoked")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("call context was not canceled")
	}

	deadline := time.Now().Add(time.Second)
	for controller.Active() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if controller.Active() {
		t.Fatal("call slot was not released")
	}
	if err := controller.CancelActive(nil); !errors.Is(err, ErrNoActiveCall) {
		t.Fatalf("idle cancel error = %v, want ErrNoActiveCall", err)
	}
}

func TestControllerRejectsCanceledParent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var controller Controller
	if err := controller.Start(ctx, func(context.Context) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context.Canceled", err)
	}
	if controller.Active() {
		t.Fatal("canceled parent must not reserve the call slot")
	}
}
