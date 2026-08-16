package sip

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegisterDialAndRemoteBye(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverAddr := server.LocalAddr().(*net.UDPAddr)
	localPort := freeUDPPort(t)

	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 65535)
		registered := false
		inviteAuthenticated := false
		for {
			n, addr, err := server.ReadFromUDP(buf)
			if err != nil {
				return
			}
			m, err := parseMessage(buf[:n])
			if err != nil {
				serverErr <- err
				return
			}
			switch m.Method {
			case "REGISTER":
				if m.Header("authorization") == "" {
					_ = mockResponse(server, addr, m, 401, "Unauthorized", []string{`WWW-Authenticate: Digest realm="fritz.box", nonce="abc", qop="auth"`}, nil)
				} else {
					registered = true
					_ = mockResponse(server, addr, m, 200, "OK", []string{"Expires: 300"}, nil)
				}
			case "INVITE":
				if !registered {
					serverErr <- fmt.Errorf("invite before registration")
					return
				}
				if m.Header("authorization") == "" {
					_ = mockResponse(server, addr, m, 401, "Unauthorized", []string{`WWW-Authenticate: Digest realm="fritz.box", nonce="def", qop="auth"`}, nil)
				} else {
					inviteAuthenticated = true
					_ = mockResponse(server, addr, m, 180, "Ringing", nil, nil)
					sdp := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"
					_ = mockResponse(server, addr, m, 200, "OK", []string{"Contact: <sip:mock@127.0.0.1:" + strconv.Itoa(serverAddr.Port) + ">", "Content-Type: application/sdp"}, []byte(sdp))
				}
			case "ACK":
				if inviteAuthenticated {
					// Send a remote BYE after the 2xx ACK.
					bye := buildMockBye(m, addr, serverAddr.Port)
					_, _ = server.WriteToUDP(bye, addr)
					inviteAuthenticated = false
				}
			case "BYE":
				_ = mockResponse(server, addr, m, 200, "OK", nil, nil)
			}
		}
	}()

	client, err := New(Config{Registrar: "127.0.0.1", RegistrarPort: serverAddr.Port, Username: "621", Password: "secret", LocalPort: localPort, DisplayName: "Door", CodecPreference: "pcma"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.register(ctx, 300); err != nil {
		t.Fatal(err)
	}
	call, err := client.Dial(ctx, "**610", 30000)
	if err != nil {
		t.Fatal(err)
	}
	if call.Codec.Name != "pcma" || call.RemoteRTP.Port != 40000 {
		t.Fatalf("bad media: %#v %v", call.Codec, call.RemoteRTP)
	}
	select {
	case err := <-call.Done():
		if err != nil {
			t.Fatal(err)
		}
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("remote BYE not processed")
	}
}

func freeUDPPort(t *testing.T) int {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return p
}

func mockResponse(conn *net.UDPConn, addr *net.UDPAddr, req Message, code int, reason string, extra []string, body []byte) error {
	to := req.Header("to")
	if !strings.Contains(strings.ToLower(to), ";tag=") {
		to += ";tag=mocktag"
	}
	lines := []string{fmt.Sprintf("SIP/2.0 %d %s", code, reason), "Via: " + req.Header("via"), "From: " + req.Header("from"), "To: " + to, "Call-ID: " + req.Header("call-id"), "CSeq: " + req.Header("cseq")}
	lines = append(lines, extra...)
	lines = append(lines, fmt.Sprintf("Content-Length: %d", len(body)), "", "")
	packet := append([]byte(strings.Join(lines, "\r\n")), body...)
	_, err := conn.WriteToUDP(packet, addr)
	return err
}

func buildMockBye(ack Message, target *net.UDPAddr, serverPort int) []byte {
	callID := ack.Header("call-id")
	from := ack.Header("to")
	to := ack.Header("from")
	lines := []string{
		fmt.Sprintf("BYE sip:621@%s:%d SIP/2.0", target.IP, target.Port),
		fmt.Sprintf("Via: SIP/2.0/UDP 127.0.0.1:%d;branch=%s", serverPort, branchID()),
		"Max-Forwards: 70", "From: " + from, "To: " + to, "Call-ID: " + callID, "CSeq: 2 BYE", "Content-Length: 0", "", "",
	}
	return []byte(strings.Join(lines, "\r\n"))
}

func TestInviteTimeoutCancelsAndACKs487(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverAddr := server.LocalAddr().(*net.UDPAddr)
	localPort := freeUDPPort(t)

	gotCancel := make(chan struct{}, 1)
	gotACK := make(chan struct{}, 1)
	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 65535)
		var invite Message
		var inviteAddr *net.UDPAddr
		for {
			n, addr, err := server.ReadFromUDP(buf)
			if err != nil {
				return
			}
			m, err := parseMessage(buf[:n])
			if err != nil {
				serverErr <- err
				return
			}
			switch m.Method {
			case "INVITE":
				if invite.Method == "" {
					invite = m
					inviteAddr = addr
					_ = mockResponse(server, addr, m, 180, "Ringing", nil, nil)
				}
			case "CANCEL":
				select {
				case gotCancel <- struct{}{}:
				default:
				}
				_ = mockResponse(server, addr, m, 200, "OK", nil, nil)
				if invite.Method != "" {
					_ = mockResponse(server, inviteAddr, invite, 487, "Request Terminated", nil, nil)
				}
			case "ACK":
				select {
				case gotACK <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	client, err := New(Config{Registrar: "127.0.0.1", RegistrarPort: serverAddr.Port, Username: "621", Password: "secret", LocalPort: localPort, DisplayName: "Door", CodecPreference: "pcma"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_, err = client.Dial(ctx, "**610", 30000)
	if err == nil || !strings.Contains(err.Error(), "487") {
		t.Fatalf("expected 487 after CANCEL, got %v", err)
	}
	select {
	case <-gotCancel:
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("CANCEL not received")
	}
	select {
	case <-gotACK:
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("ACK for 487 not received")
	}
}

func TestSuccessfulInviteWithBadSDPIsACKedThenHungUp(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverAddr := server.LocalAddr().(*net.UDPAddr)
	localPort := freeUDPPort(t)
	gotACK := make(chan struct{}, 1)
	gotBYE := make(chan struct{}, 1)
	serverErr := make(chan error, 1)

	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := server.ReadFromUDP(buf)
			if err != nil {
				return
			}
			m, err := parseMessage(buf[:n])
			if err != nil {
				serverErr <- err
				return
			}
			switch m.Method {
			case "INVITE":
				badSDP := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=video 40002 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n"
				_ = mockResponse(server, addr, m, 200, "OK", []string{"Contact: <sip:mock@127.0.0.1:" + strconv.Itoa(serverAddr.Port) + ">", "Content-Type: application/sdp"}, []byte(badSDP))
			case "ACK":
				select {
				case gotACK <- struct{}{}:
				default:
				}
			case "BYE":
				select {
				case gotBYE <- struct{}{}:
				default:
				}
				_ = mockResponse(server, addr, m, 200, "OK", nil, nil)
				return
			}
		}
	}()

	client, err := New(Config{Registrar: "127.0.0.1", RegistrarPort: serverAddr.Port, Username: "621", Password: "secret", LocalPort: localPort, DisplayName: "Door", CodecPreference: "pcma"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = client.Dial(ctx, "**610", 30000)
	if err == nil || !strings.Contains(err.Error(), "invalid SIP media answer") {
		t.Fatalf("expected media negotiation error, got %v", err)
	}
	select {
	case <-gotACK:
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("2xx INVITE was not ACKed")
	}
	select {
	case <-gotBYE:
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("bad media dialog was not cleaned up with BYE")
	}
}

func TestLate200AfterRingTimeoutIsACKedAndHungUp(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverAddr := server.LocalAddr().(*net.UDPAddr)
	localPort := freeUDPPort(t)
	gotACK := make(chan struct{}, 1)
	gotBYE := make(chan struct{}, 1)
	serverErr := make(chan error, 1)

	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := server.ReadFromUDP(buf)
			if err != nil {
				return
			}
			m, err := parseMessage(buf[:n])
			if err != nil {
				serverErr <- err
				return
			}
			switch m.Method {
			case "INVITE":
				// Do not send a provisional response. Let the caller's ring timeout
				// expire, then race a successful final response across it.
				time.Sleep(180 * time.Millisecond)
				sdp := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"
				_ = mockResponse(server, addr, m, 200, "OK", []string{"Contact: <sip:mock@127.0.0.1:" + strconv.Itoa(serverAddr.Port) + ">", "Content-Type: application/sdp"}, []byte(sdp))
			case "ACK":
				select {
				case gotACK <- struct{}{}:
				default:
				}
			case "BYE":
				select {
				case gotBYE <- struct{}{}:
				default:
				}
				_ = mockResponse(server, addr, m, 200, "OK", nil, nil)
				return
			}
		}
	}()

	client, err := New(Config{Registrar: "127.0.0.1", RegistrarPort: serverAddr.Port, Username: "621", Password: "secret", LocalPort: localPort, DisplayName: "Door", CodecPreference: "pcma"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_, err = client.Dial(ctx, "**610", 30000)
	if err == nil || !strings.Contains(err.Error(), "answered after cancellation") {
		t.Fatalf("expected late-answer cancellation error, got %v", err)
	}
	select {
	case <-gotACK:
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("late 200 OK was not ACKed")
	}
	select {
	case <-gotBYE:
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("late answered dialog was not closed with BYE")
	}
}
