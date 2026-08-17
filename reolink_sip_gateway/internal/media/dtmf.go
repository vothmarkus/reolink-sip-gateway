package media

import (
	"encoding/binary"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
)

const completedTelephoneEventWindow = 64

// DTMFEvent is one completed RFC 4733 telephone event received from the SIP
// peer. It is intentionally transient and never becomes persistent call state.
type DTMFEvent struct {
	Digit      string
	DurationMS int
	ReceivedAt time.Time
}

type telephoneEventKey struct {
	ssrc      uint32
	timestamp uint32
	event     uint8
}

type telephoneEventDetector struct {
	clockRate int
	completed map[telephoneEventKey]struct{}
	order     []telephoneEventKey
}

func newTelephoneEventDetector(clockRate int) *telephoneEventDetector {
	return &telephoneEventDetector{
		clockRate: clockRate,
		completed: make(map[telephoneEventKey]struct{}),
	}
}

// Push emits only the first terminal packet for an event. RFC 4733 repeats the
// terminal packet for reliability, so the RTP timestamp/event/SSRC tuple is
// retained in a small bounded window to prevent duplicate Home Assistant
// events while still tolerating packet reordering.
func (d *telephoneEventDetector) Push(packet rtp.Packet, receivedAt time.Time) (DTMFEvent, bool) {
	if d == nil || d.clockRate <= 0 || len(packet.Payload) < 4 {
		return DTMFEvent{}, false
	}
	eventCode := packet.Payload[0]
	digit, ok := dtmfDigit(eventCode)
	if !ok || packet.Payload[1]&0x40 != 0 || packet.Payload[1]&0x80 == 0 {
		return DTMFEvent{}, false
	}
	duration := binary.BigEndian.Uint16(packet.Payload[2:4])
	if duration == 0 {
		return DTMFEvent{}, false
	}
	key := telephoneEventKey{ssrc: packet.SSRC, timestamp: packet.Timestamp, event: eventCode}
	if _, duplicate := d.completed[key]; duplicate {
		return DTMFEvent{}, false
	}
	d.completed[key] = struct{}{}
	d.order = append(d.order, key)
	if len(d.order) > completedTelephoneEventWindow {
		delete(d.completed, d.order[0])
		d.order = d.order[1:]
	}

	durationMS := int((int64(duration)*1000 + int64(d.clockRate)/2) / int64(d.clockRate))
	return DTMFEvent{Digit: digit, DurationMS: durationMS, ReceivedAt: receivedAt.UTC()}, true
}

func dtmfDigit(event uint8) (string, bool) {
	switch {
	case event <= 9:
		return string(rune('0' + event)), true
	case event == 10:
		return "*", true
	case event == 11:
		return "#", true
	case event >= 12 && event <= 15:
		return string(rune('A' + event - 12)), true
	default:
		return "", false
	}
}
