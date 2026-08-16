package rtp

import "testing"

func TestRoundTrip(t *testing.T) {
	in := Packet{PayloadType: 8, Marker: true, Sequence: 42, Timestamp: 99, SSRC: 1234, Payload: []byte{1, 2, 3}}
	out, err := Parse(Marshal(in))
	if err != nil {
		t.Fatal(err)
	}
	if out.PayloadType != in.PayloadType || out.Sequence != in.Sequence || out.Timestamp != in.Timestamp || out.SSRC != in.SSRC || len(out.Payload) != 3 {
		t.Fatalf("roundtrip mismatch: %#v", out)
	}
}
