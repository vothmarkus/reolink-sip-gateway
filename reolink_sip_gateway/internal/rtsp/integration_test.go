package rtsp

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	rtppkg "github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
)

func TestOpenDigestAndWriteBackchannel(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)
	gotRTP := make(chan rtppkg.Packet, 1)
	gotTeardown := make(chan struct{}, 1)
	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		authed := false
		for {
			first, err := r.Peek(1)
			if err != nil {
				serverErr <- err
				return
			}
			if first[0] == '$' {
				h := make([]byte, 4)
				if _, err = io.ReadFull(r, h); err != nil {
					serverErr <- err
					return
				}
				n := int(binary.BigEndian.Uint16(h[2:4]))
				b := make([]byte, n)
				if _, err = io.ReadFull(r, b); err != nil {
					serverErr <- err
					return
				}
				if h[1] == 0 {
					p, err := rtppkg.Parse(b)
					if err != nil {
						serverErr <- err
						return
					}
					select {
					case gotRTP <- p:
					default:
					}
				}
				continue
			}
			req, err := readMockRTSPRequest(r)
			if err != nil {
				serverErr <- err
				return
			}
			cseq := req.headers["cseq"]
			switch req.method {
			case "DESCRIBE":
				if req.headers["authorization"] == "" {
					_ = writeMockRTSP(conn, 401, "Unauthorized", cseq, map[string]string{"WWW-Authenticate": `Digest realm="reolink", nonce="abc", qop="auth"`}, nil)
				} else {
					authed = true
					sdp := "v=0\r\nm=audio 0 RTP/AVP 97\r\na=recvonly\r\na=rtpmap:97 MPEG4-GENERIC/16000/1\r\na=control:trackID=1\r\nm=audio 0 RTP/AVP 0\r\na=sendonly\r\na=rtpmap:0 PCMU/8000\r\na=control:trackID=2\r\n"
					_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Content-Type": "application/sdp", "Content-Base": fmt.Sprintf("rtsp://127.0.0.1:%d/live/", addr.Port)}, []byte(sdp))
				}
			case "SETUP":
				if !authed {
					serverErr <- fmt.Errorf("setup without auth")
					return
				}
				_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Session": "1234;timeout=60", "Transport": "RTP/AVP/TCP;unicast;interleaved=0-1"}, nil)
			case "PLAY":
				_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Session": "1234"}, nil)
			case "GET_PARAMETER":
				_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Session": "1234"}, nil)
			case "TEARDOWN":
				_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Session": "1234"}, nil)
				select {
				case gotTeardown <- struct{}{}:
				default:
				}
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := New(fmt.Sprintf("rtsp://127.0.0.1:%d/live", addr.Port), "user", "pass", nil, false)
	bc, err := c.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bc.Codec != "pcmu" || bc.PayloadType != 0 {
		t.Fatalf("bad backchannel %#v", bc)
	}
	if err := c.WriteAudio([]byte{1, 2, 3, 4}, bc.PayloadType); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-gotRTP:
		if p.PayloadType != 0 || len(p.Payload) != 4 {
			t.Fatalf("bad RTP %#v", p)
		}
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("no interleaved RTP")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := c.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gotTeardown:
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("TEARDOWN not received")
	}
}

type mockRTSPRequest struct {
	method  string
	uri     string
	headers map[string]string
	body    []byte
}

func readMockRTSPRequest(r *bufio.Reader) (mockRTSPRequest, error) {
	line, err := readLine(r)
	if err != nil {
		return mockRTSPRequest{}, err
	}
	f := strings.Fields(line)
	if len(f) < 3 {
		return mockRTSPRequest{}, fmt.Errorf("bad request %q", line)
	}
	h := map[string]string{}
	for {
		line, err = readLine(r)
		if err != nil {
			return mockRTSPRequest{}, err
		}
		if line == "" {
			break
		}
		p := strings.SplitN(line, ":", 2)
		if len(p) == 2 {
			h[strings.ToLower(strings.TrimSpace(p[0]))] = strings.TrimSpace(p[1])
		}
	}
	n, _ := strconv.Atoi(h["content-length"])
	body := make([]byte, n)
	if n > 0 {
		_, err = io.ReadFull(r, body)
	}
	return mockRTSPRequest{method: f[0], uri: f[1], headers: h, body: body}, err
}
func writeMockRTSP(w io.Writer, code int, reason, cseq string, headers map[string]string, body []byte) error {
	var b strings.Builder
	fmt.Fprintf(&b, "RTSP/1.0 %d %s\r\nCSeq: %s\r\n", code, reason, cseq)
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func TestOpenDigestRetriesStaleNonceAndImplicitMD5(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)
	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		describeCount := 0
		for {
			req, err := readMockRTSPRequest(r)
			if err != nil {
				serverErr <- err
				return
			}
			cseq := req.headers["cseq"]
			switch req.method {
			case "DESCRIBE":
				describeCount++
				auth := req.headers["authorization"]
				switch describeCount {
				case 1:
					_ = writeMockRTSP(conn, 401, "Unauthorized", cseq, map[string]string{"WWW-Authenticate": `Digest realm="reolink", nonce="one", qop="auth"`}, nil)
				case 2:
					if auth == "" {
						serverErr <- fmt.Errorf("second DESCRIBE missing Authorization")
						return
					}
					if strings.Contains(strings.ToLower(auth), "algorithm=") {
						serverErr <- fmt.Errorf("implicit MD5 algorithm was echoed: %s", auth)
						return
					}
					_ = writeMockRTSP(conn, 401, "Unauthorized", cseq, map[string]string{"WWW-Authenticate": `Digest realm="reolink", nonce="two", qop="auth", stale=true`}, nil)
				case 3:
					if auth == "" {
						serverErr <- fmt.Errorf("third DESCRIBE missing Authorization")
						return
					}
					sdp := "v=0\r\nm=audio 0 RTP/AVP 8\r\na=sendonly\r\na=control:trackID=2\r\n"
					_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Content-Type": "application/sdp", "Content-Base": fmt.Sprintf("rtsp://127.0.0.1:%d/live/", addr.Port)}, []byte(sdp))
				default:
					serverErr <- fmt.Errorf("unexpected DESCRIBE count %d", describeCount)
					return
				}
			case "SETUP":
				_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Session": "1234;timeout=60", "Transport": "RTP/AVP/TCP;unicast;interleaved=0-1"}, nil)
			case "PLAY":
				_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Session": "1234"}, nil)
			case "TEARDOWN":
				_ = writeMockRTSP(conn, 200, "OK", cseq, map[string]string{"Session": "1234"}, nil)
				serverErr <- nil
				return
			default:
				serverErr <- fmt.Errorf("unexpected RTSP method %s", req.method)
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := New(fmt.Sprintf("rtsp://127.0.0.1:%d/live", addr.Port), "user", "pass", nil, false)
	bc, err := c.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bc.Codec != "pcma" || bc.PayloadType != 8 {
		t.Fatalf("bad backchannel %#v", bc)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := c.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock server did not finish")
	}
}
