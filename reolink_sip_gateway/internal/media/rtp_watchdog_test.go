package media

import (
	"errors"
	"testing"
	"time"
)

func TestRTPWatchdogTracksValidPacketActivity(t *testing.T) {
	start := time.Unix(100, 0)
	watchdog := newRTPWatchdog(15*time.Second, start)
	if err := watchdog.Check(start.Add(14 * time.Second)); err != nil {
		t.Fatalf("watchdog expired early: %v", err)
	}
	watchdog.Mark(start.Add(10 * time.Second))
	if err := watchdog.Check(start.Add(24 * time.Second)); err != nil {
		t.Fatalf("watchdog ignored refreshed activity: %v", err)
	}
	if err := watchdog.Check(start.Add(25 * time.Second)); !errors.Is(err, ErrRTPInactivity) {
		t.Fatalf("watchdog result=%v", err)
	}
}

func TestDisabledRTPWatchdogNeverExpires(t *testing.T) {
	start := time.Unix(100, 0)
	if err := newRTPWatchdog(0, start).Check(start.Add(24 * time.Hour)); err != nil {
		t.Fatalf("disabled watchdog expired: %v", err)
	}
}
