package media

import (
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
)

func TestTelephoneEventDetectorEmitsCompletedDigitOnce(t *testing.T) {
	detector := newTelephoneEventDetector(8000)
	receivedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	start := rtp.Packet{SSRC: 7, Timestamp: 1234, Payload: []byte{5, 0x0a, 0x01, 0x40}}
	if _, ok := detector.Push(start, receivedAt); ok {
		t.Fatal("non-terminal telephone event emitted a digit")
	}

	end := start
	end.Payload = []byte{5, 0x8a, 0x03, 0x20}
	event, ok := detector.Push(end, receivedAt)
	if !ok {
		t.Fatal("terminal telephone event was not emitted")
	}
	if event.Digit != "5" || event.DurationMS != 100 || !event.ReceivedAt.Equal(receivedAt) || event.ReceivedAt.Location() != time.UTC {
		t.Fatalf("unexpected DTMF event: %#v", event)
	}
	if _, ok := detector.Push(end, receivedAt.Add(time.Millisecond)); ok {
		t.Fatal("repeated terminal packet emitted a duplicate digit")
	}
}

func TestTelephoneEventDetectorMapsAllDTMFDigits(t *testing.T) {
	want := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "#", "A", "B", "C", "D"}
	detector := newTelephoneEventDetector(8000)
	for code, digit := range want {
		packet := rtp.Packet{
			SSRC:      1,
			Timestamp: uint32(1000 + code),
			Payload:   []byte{byte(code), 0x80, 0x00, 0xa0},
		}
		event, ok := detector.Push(packet, time.Now())
		if !ok || event.Digit != digit || event.DurationMS != 20 {
			t.Fatalf("event %d = %#v, emitted=%t; want %q/20 ms", code, event, ok, digit)
		}
	}
}

func TestTelephoneEventDetectorRejectsMalformedAndUnsupportedEvents(t *testing.T) {
	tests := []rtp.Packet{
		{Payload: []byte{1, 0x80, 0x00}},
		{Payload: []byte{16, 0x80, 0x00, 0xa0}},
		{Payload: []byte{1, 0xc0, 0x00, 0xa0}},
		{Payload: []byte{1, 0x80, 0x00, 0x00}},
	}
	for _, packet := range tests {
		if event, ok := newTelephoneEventDetector(8000).Push(packet, time.Now()); ok {
			t.Fatalf("malformed event emitted %#v", event)
		}
	}
}
