package trigger

import "testing"

func TestVisitorSnapshotsAndRepeatedStatesDoNotCall(t *testing.T) {
	var edge visitorEdge
	states := []bool{true, true, false, false, true, true, false, true}
	want := []bool{false, false, false, false, true, false, false, true}
	for i, state := range states {
		if got := edge.update(state); got != want[i] {
			t.Fatalf("state %d: got %v want %v", i, got, want[i])
		}
	}
	// A reconnect establishes a new baseline, rather than replaying a press.
	edge = visitorEdge{}
	if edge.update(true) {
		t.Fatal("reconnect snapshot caused a call")
	}
}
