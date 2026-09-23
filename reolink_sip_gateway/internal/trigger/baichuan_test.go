package trigger

import (
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
