package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/liveimage"
)

type recordingLiveImageDiscoverer struct {
	calls  atomic.Int32
	called chan struct{}
}

func (d *recordingLiveImageDiscoverer) Discover(context.Context) ([]liveimage.Channel, error) {
	d.calls.Add(1)
	select {
	case d.called <- struct{}{}:
	default:
	}
	return []liveimage.Channel{{Number: 1}}, nil
}

func TestLiveImageDiscoveryRunsImmediatelyAndPeriodically(t *testing.T) {
	discoverer := &recordingLiveImageDiscoverer{called: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLiveImageDiscovery(ctx, discoverer, 10*time.Millisecond, time.Second, nil)
		close(done)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-discoverer.called:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for channel discovery")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("discovery scheduler did not stop")
	}
	if discoverer.calls.Load() < 2 {
		t.Fatalf("discovery calls=%d", discoverer.calls.Load())
	}
}

type recordingLiveImagePrewarmer struct {
	called chan time.Time
}

func (p recordingLiveImagePrewarmer) PrewarmPrimary(ctx context.Context) error {
	deadline, _ := ctx.Deadline()
	p.called <- deadline
	return nil
}

func TestLiveImagePrewarmStartsAsynchronouslyWithDeadline(t *testing.T) {
	prewarmer := recordingLiveImagePrewarmer{called: make(chan time.Time, 1)}
	started := time.Now()
	startLiveImagePrewarm(context.Background(), prewarmer, nil)
	select {
	case deadline := <-prewarmer.called:
		if deadline.Before(started.Add(liveImagePrewarmTimeout-time.Second)) || deadline.After(time.Now().Add(liveImagePrewarmTimeout+time.Second)) {
			t.Fatalf("unexpected prewarm deadline %s", deadline)
		}
	case <-time.After(time.Second):
		t.Fatal("prewarm was not started")
	}
}
