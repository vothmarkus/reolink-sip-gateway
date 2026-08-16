package rtsp

import (
	"bufio"
	"strings"
	"testing"
)

func TestFindBackchannel(t *testing.T) {
	sdp := "v=0\r\n" +
		"m=video 0 RTP/AVP 96\r\na=recvonly\r\na=control:trackID=0\r\n" +
		"m=audio 0 RTP/AVP 97\r\na=recvonly\r\na=rtpmap:97 MPEG4-GENERIC/16000/1\r\na=control:trackID=1\r\n" +
		"m=audio 0 RTP/AVP 0\r\na=sendonly\r\na=rtpmap:0 PCMU/8000\r\na=control:trackID=2\r\n"
	bc, err := findBackchannel(sdp, "rtsp://cam/live/")
	if err != nil {
		t.Fatal(err)
	}
	if bc.Codec != "pcmu" || bc.PayloadType != 0 || bc.ControlURL != "rtsp://cam/live/trackID=2" {
		t.Fatalf("unexpected: %#v", bc)
	}
}

func TestReadResponseRejectsInvalidContentLength(t *testing.T) {
	for _, raw := range []string{
		"RTSP/1.0 200 OK\r\nCSeq: 1\r\nContent-Length: -1\r\n\r\n",
		"RTSP/1.0 200 OK\r\nCSeq: 1\r\nContent-Length: 999999999\r\n\r\n",
		"RTSP/1.0 200 OK\r\nCSeq: 1\r\nContent-Length: nope\r\n\r\n",
	} {
		if _, err := readResponse(bufio.NewReader(strings.NewReader(raw))); err == nil {
			t.Fatalf("expected Content-Length rejection for %q", raw)
		}
	}
}

func TestFindBackchannelAcceptsStaticPayloadWithoutRTPMap(t *testing.T) {
	sdp := "v=0\r\nm=audio 0 RTP/AVP 0\r\na=control:back\r\na=sendonly\r\n"
	bc, err := findBackchannel(sdp, "rtsp://192.0.2.1/stream/")
	if err != nil {
		t.Fatal(err)
	}
	if bc.Codec != "pcmu" || bc.PayloadType != 0 {
		t.Fatalf("unexpected backchannel: %#v", bc)
	}
}

func TestReadResponsePreservesRepeatedAuthenticateHeaders(t *testing.T) {
	raw := "RTSP/1.0 401 Unauthorized\r\nCSeq: 1\r\n" +
		"WWW-Authenticate: Digest realm=\"cam\", nonce=\"one\", algorithm=SHA-256, qop=\"auth\"\r\n" +
		"WWW-Authenticate: Digest realm=\"cam\", nonce=\"two\", algorithm=MD5, qop=\"auth\"\r\n" +
		"Content-Length: 0\r\n\r\n"
	res, err := readResponse(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	values := responseHeaderValues(res, "WWW-Authenticate")
	if len(values) != 2 {
		t.Fatalf("got %d auth headers: %#v", len(values), values)
	}
	ch, err := chooseDigestChallenge(values)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Algorithm != "SHA-256" {
		t.Fatalf("expected first supported challenge, got %#v", ch)
	}
}
