package media

import (
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
)

// rtpMediaClock maps normalized G.711 RTP timestamps to a continuous wall-clock
// timeline. The local FFmpeg RTP muxer may emit variable payload sizes or short
// bursts; using packet arrival time directly would make two adjacent 20 ms
// chunks appear simultaneous to the echo aligner.
type rtpMediaClock struct {
	initialized bool
	baseTS      uint32
	baseAt      time.Time
}

func (c *rtpMediaClock) At(pkt rtp.Packet, arrival time.Time) time.Time {
	if !c.initialized || pkt.Marker {
		c.initialized = true
		c.baseTS = pkt.Timestamp
		c.baseAt = arrival
		return arrival
	}
	// Signed modular subtraction is valid for realistic call durations and also
	// handles the normal uint32 RTP timestamp wrap-around.
	delta := int64(int32(pkt.Timestamp - c.baseTS))
	return c.baseAt.Add(time.Duration(delta) * time.Second / g711SampleRate)
}
