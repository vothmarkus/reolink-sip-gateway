package media

import (
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
)

func TestRTPMediaClockUsesMediaTimestampAcrossArrivalBurst(t *testing.T) {
	base := time.Unix(1000, 0)
	var c rtpMediaClock
	p0 := rtp.Packet{Timestamp: 10000, Marker: true}
	p1 := rtp.Packet{Timestamp: 10160}
	p2 := rtp.Packet{Timestamp: 10320}
	if got := c.At(p0, base); !got.Equal(base) {
		t.Fatalf("first=%s want %s", got, base)
	}
	// All three packets can arrive in the same burst; their media clock must
	// nevertheless retain the 20 ms G.711 cadence.
	if got := c.At(p1, base.Add(time.Millisecond)); !got.Equal(base.Add(20 * time.Millisecond)) {
		t.Fatalf("second=%s", got)
	}
	if got := c.At(p2, base.Add(time.Millisecond)); !got.Equal(base.Add(40 * time.Millisecond)) {
		t.Fatalf("third=%s", got)
	}
}

func TestRTPMediaClockResetsOnMarker(t *testing.T) {
	base := time.Unix(1000, 0)
	var c rtpMediaClock
	_ = c.At(rtp.Packet{Timestamp: 100, Marker: true}, base)
	resetAt := base.Add(3 * time.Second)
	got := c.At(rtp.Packet{Timestamp: 999999, Marker: true}, resetAt)
	if !got.Equal(resetAt) {
		t.Fatalf("reset=%s want %s", got, resetAt)
	}
}
