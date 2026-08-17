package sip

import (
	"net"
	"testing"
)

func TestParseAnswerSDP(t *testing.T) {
	sdp := "v=0\r\nc=IN IP4 192.0.2.5\r\nm=audio 4000 RTP/AVP 8 101\r\na=rtpmap:8 PCMA/8000\r\n"
	c, a, err := parseAnswerSDP(sdp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "pcma" || c.PayloadType != 8 || a.Port != 4000 {
		t.Fatalf("bad parse %#v %v", c, a)
	}
}

func TestParseAnswerSDPUsesAudioMediaConnection(t *testing.T) {
	sdp := "v=0\r\nc=IN IP4 192.0.2.1\r\nm=audio 4000 RTP/AVP 8\r\nc=IN IP4 192.0.2.5\r\na=rtpmap:8 PCMA/8000\r\nm=video 5000 RTP/AVP 96\r\nc=IN IP4 192.0.2.99\r\n"
	c, a, err := parseAnswerSDP(sdp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "pcma" || !a.IP.Equal(net.ParseIP("192.0.2.5")) || a.Port != 4000 {
		t.Fatalf("bad media-level address selection: %#v %v", c, a)
	}
}
func TestMessageParse(t *testing.T) {
	m, err := parseMessage([]byte("SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP x;branch=z9\r\nCSeq: 1 INVITE\r\nContent-Length: 0\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsResponse || m.StatusCode != 200 || cseqMethod(m.Header("cseq")) != "INVITE" {
		t.Fatalf("bad %#v", m)
	}
}

func TestParseOfferSDPHonorsCodecPreference(t *testing.T) {
	sdp := "v=0\r\nc=IN IP4 192.0.2.5\r\nm=audio 4000 RTP/AVP 0 8 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\n"
	codec, remote, err := parseOfferSDP(sdp, "pcma")
	if err != nil {
		t.Fatal(err)
	}
	if codec.Name != "pcma" || codec.PayloadType != 8 || remote.Port != 4000 {
		t.Fatalf("unexpected preferred offer selection: %#v %v", codec, remote)
	}
	codec, _, err = parseOfferSDP(sdp, "pcmu")
	if err != nil {
		t.Fatal(err)
	}
	if codec.Name != "pcmu" || codec.PayloadType != 0 {
		t.Fatalf("unexpected PCMU offer selection: %#v", codec)
	}
}

func TestParseOfferSDPRejectsUnsupportedOrHeldAudio(t *testing.T) {
	for _, sdp := range []string{
		"v=0\r\nc=IN IP4 192.0.2.5\r\nm=audio 4000 RTP/AVP 111\r\na=rtpmap:111 opus/48000/2\r\n",
		"v=0\r\nc=IN IP4 0.0.0.0\r\nm=audio 4000 RTP/AVP 8\r\n",
	} {
		if _, _, err := parseOfferSDP(sdp, "pcma"); err == nil {
			t.Fatalf("expected offer rejection for %q", sdp)
		}
	}
}
