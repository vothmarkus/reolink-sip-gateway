package sip

import (
	"net"
	"testing"
	"time"
)

func TestParseAnswerSDP(t *testing.T) {
	sdp := "v=0\r\nc=IN IP4 192.0.2.5\r\nm=audio 4000 RTP/AVP 8 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\n"
	media, err := parseAnswerMedia(sdp)
	if err != nil {
		t.Fatal(err)
	}
	if media.Codec.Name != "pcma" || media.Codec.PayloadType != 8 || media.RemoteRTP.Port != 4000 {
		t.Fatalf("bad parse %#v", media)
	}
	if media.TelephoneEvent == nil || media.TelephoneEvent.PayloadType != 101 || media.TelephoneEvent.ClockRate != 8000 {
		t.Fatalf("telephone-event was not negotiated: %#v", media.TelephoneEvent)
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

func TestCanceledInviteTransactionHandoff(t *testing.T) {
	headers := []string{
		"From: <sip:mobile@fritz.box>;tag=from-tag",
		"To: <sip:0163@fritz.box>",
		"Call-ID: canceled-call@fritz.box",
	}

	t.Run("buffered final response stays with transaction", func(t *testing.T) {
		responses := make(chan Message, 2)
		responses <- Message{StatusCode: 180}
		responses <- Message{StatusCode: 487}
		client := &Client{
			transactions:    map[string]chan Message{"branch|INVITE": responses},
			canceledInvites: make(map[string]*canceledInvite),
			closed:          make(chan struct{}),
		}

		response, received := client.transitionCanceledInvite("branch|INVITE", responses, "sip:0163@fritz.box", "branch", 1, headers, true)
		if !received || response.StatusCode != 487 {
			t.Fatalf("expected buffered final response, got received=%t response=%#v", received, response)
		}
		if len(client.transactions) != 0 || len(client.canceledInvites) != 0 {
			t.Fatalf("unexpected handoff state: transactions=%d canceled=%d", len(client.transactions), len(client.canceledInvites))
		}
	})

	t.Run("provisional response moves to tombstone", func(t *testing.T) {
		responses := make(chan Message, 1)
		responses <- Message{StatusCode: 180}
		client := &Client{
			transactions:    map[string]chan Message{"branch|INVITE": responses},
			canceledInvites: make(map[string]*canceledInvite),
			closed:          make(chan struct{}),
		}

		_, received := client.transitionCanceledInvite("branch|INVITE", responses, "sip:0163@fritz.box", "branch", 1, headers, true)
		if received {
			t.Fatal("provisional response must not finish the INVITE transaction")
		}
		invite := client.canceledInvites["canceled-call@fritz.box"]
		if len(client.transactions) != 0 || invite == nil || !invite.cancelSent {
			t.Fatalf("unexpected handoff state: transactions=%d invite=%#v", len(client.transactions), invite)
		}

		close(client.closed)
		deadline := time.Now().Add(time.Second)
		for {
			client.mu.Lock()
			remaining := len(client.canceledInvites)
			client.mu.Unlock()
			if remaining == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("canceled INVITE tombstone did not expire on client close")
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func TestParseOfferSDPHonorsCodecPreference(t *testing.T) {
	sdp := "v=0\r\nc=IN IP4 192.0.2.5\r\nm=audio 4000 RTP/AVP 0 8 110\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:110 telephone-event/8000\r\n"
	media, err := parseOfferMedia(sdp, "pcma")
	if err != nil {
		t.Fatal(err)
	}
	if media.Codec.Name != "pcma" || media.Codec.PayloadType != 8 || media.RemoteRTP.Port != 4000 {
		t.Fatalf("unexpected preferred offer selection: %#v", media)
	}
	if media.TelephoneEvent == nil || media.TelephoneEvent.PayloadType != 110 {
		t.Fatalf("unexpected telephone-event selection: %#v", media.TelephoneEvent)
	}
	codec, _, err := parseOfferSDP(sdp, "pcmu")
	if err != nil {
		t.Fatal(err)
	}
	if codec.Name != "pcmu" || codec.PayloadType != 0 {
		t.Fatalf("unexpected PCMU offer selection: %#v", codec)
	}
}

func TestTelephoneEventNegotiationRequiresEightKilohertzAndOfferedAnswerPayload(t *testing.T) {
	for _, sdp := range []string{
		"v=0\r\nc=IN IP4 192.0.2.5\r\nm=audio 4000 RTP/AVP 8 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/16000\r\n",
		"v=0\r\nc=IN IP4 192.0.2.5\r\nm=audio 4000 RTP/AVP 8 110\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:110 telephone-event/8000\r\n",
	} {
		media, err := parseAnswerMedia(sdp)
		if err != nil {
			t.Fatal(err)
		}
		if media.TelephoneEvent != nil {
			t.Fatalf("invalid answer negotiated telephone-event: %#v", media.TelephoneEvent)
		}
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
