package trigger

import (
	"context"
	"errors"
	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
	"net"
	"strings"
	"testing"
	"time"
)

func TestVisitorInitialStateAndEdges(t *testing.T) {
	start := time.Unix(100, 0)
	tests := []struct {
		name         string
		times        []time.Duration
		states, want []bool
	}{
		{"startup snapshot", []time.Duration{0, time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second}, []bool{true, true, false, true, true}, []bool{false, false, false, true, false}},
		{"no initial snapshot", []time.Duration{time.Minute, time.Minute + time.Second, 2 * time.Minute, 3 * time.Minute}, []bool{true, true, false, true}, []bool{true, false, false, true}},
		{"press after idle during startup", []time.Duration{0, time.Millisecond}, []bool{false, true}, []bool{false, true}},
		{"reconnect active snapshot", []time.Duration{time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second}, []bool{true, true, false, true}, []bool{false, false, false, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edge := visitorEdge{snapshotUntil: start.Add(2 * time.Second)}
			for i, state := range tt.states {
				if got := edge.update(state, start.Add(tt.times[i])); got != tt.want[i] {
					t.Fatalf("event %d: got %v want %v", i, got, tt.want[i])
				}
			}
		})
	}
}

func TestFailedConnectionPublishesStageErrorAndRetry(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	diagnostics := make(chan Diagnostics, 32)
	listener := &Baichuan{Config: baichuan.Config{Host: "127.0.0.1", Port: port}, OnDiagnostics: func(d Diagnostics) {
		diagnostics <- d
		if d.ConnectionAttempts == 2 && d.Stage == "connecting" {
			cancel()
		}
	}}
	err = listener.Run(ctx, make(chan Event, 1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("listener failed to retry/stop: %v", err)
	}
	close(diagnostics)
	found := false
	for d := range diagnostics {
		if d.Stage != "retry_wait" {
			continue
		}
		found = true
		if d.ConnectionAttempts != 1 || d.LastErrorStage != "connecting" || d.LastError == "" || d.LastAttemptAt == nil || d.LastErrorAt == nil || d.NextRetryAt == nil {
			t.Fatalf("incomplete failure diagnostic: %+v", d)
		}
		if !d.NextRetryAt.After(*d.LastErrorAt) {
			t.Fatal("retry deadline missing")
		}
	}
	if !found {
		t.Fatal("connection failure stayed invisible")
	}
}

func TestConnectionDiagnosticsRedactCredentials(t *testing.T) {
	cfg := baichuan.Config{Username: "my-user", Password: "my-secret"}
	message := publicConnectionError(errors.New("failure my-user my-secret "+strings.Repeat("x", 1000)), cfg)
	if strings.Contains(message, cfg.Username) || strings.Contains(message, cfg.Password) || len(message) > 512 {
		t.Fatal("unsafe diagnostic")
	}
}
