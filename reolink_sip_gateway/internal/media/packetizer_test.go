package media

import (
	"bytes"
	"testing"

	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
)

func TestRTPRepacketizerFixed20ms(t *testing.T) {
	p := &rtpRepacketizer{}
	ssrc := uint32(0x12345678)
	seq := uint16(100)
	ts := uint32(9000)
	var got []rtp.Packet
	// Mimic the variable sizes observed from FFmpeg's RTP muxer.
	for _, n := range []int{160, 160, 160, 16, 160, 160, 160, 32, 112} {
		payload := bytes.Repeat([]byte{byte(n)}, n)
		got = append(got, p.Push(rtp.Packet{Sequence: seq, Timestamp: ts, SSRC: ssrc, Payload: payload}, 8)...)
		seq++
		ts += uint32(n)
	}
	if len(got) != 7 { // 1120 total samples / 160
		t.Fatalf("got %d packets, want 7", len(got))
	}
	for i, pkt := range got {
		if len(pkt.Payload) != 160 {
			t.Fatalf("packet %d payload = %d, want 160", i, len(pkt.Payload))
		}
		if pkt.PayloadType != 8 {
			t.Fatalf("packet %d payload type = %d, want 8", i, pkt.PayloadType)
		}
		if pkt.Sequence != 100+uint16(i) {
			t.Fatalf("packet %d sequence = %d", i, pkt.Sequence)
		}
		if pkt.Timestamp != 9000+uint32(i*160) {
			t.Fatalf("packet %d timestamp = %d", i, pkt.Timestamp)
		}
	}
	if !got[0].Marker || got[1].Marker {
		t.Fatal("marker must be set only on first packet after (re)start")
	}
}

func TestRTPRepacketizerDropsPartialOnDiscontinuity(t *testing.T) {
	p := &rtpRepacketizer{}
	if out := p.Push(rtp.Packet{Sequence: 1, Timestamp: 100, SSRC: 1, Payload: make([]byte, 80)}, 8); len(out) != 0 {
		t.Fatal("unexpected packet from partial input")
	}
	out := p.Push(rtp.Packet{Sequence: 2, Timestamp: 999, SSRC: 1, Payload: make([]byte, 160)}, 8)
	if len(out) != 1 || out[0].Timestamp != 999 || len(out[0].Payload) != 160 {
		t.Fatalf("unexpected repacketized output: %#v", out)
	}
}

func TestG711Chunker(t *testing.T) {
	c := &g711Chunker{}
	if got := c.Push(make([]byte, 100)); len(got) != 0 {
		t.Fatalf("got %d chunks from partial input", len(got))
	}
	got := c.Push(make([]byte, 380))
	if len(got) != 3 { // 480 bytes total
		t.Fatalf("got %d chunks, want 3", len(got))
	}
	for _, chunk := range got {
		if len(chunk) != 160 {
			t.Fatalf("chunk size = %d", len(chunk))
		}
	}
}
