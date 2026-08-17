package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIncomingInviteAnswerACKAndRemoteBye(t *testing.T) {
	registrar, client, clientAddr := newIncomingTestClientWithCallers(t, true, []string{"**610"})
	sdp := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=phone\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0 8 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\n"
	invitePacket, inviteReq := buildMockIncomingInvite(t, clientAddr, registrar.LocalAddr().(*net.UDPAddr), "incoming-1", "z9hG4bK-incoming-1", sdp)
	if _, err := registrar.WriteToUDP(invitePacket, clientAddr); err != nil {
		t.Fatal(err)
	}
	_ = readSIPResponse(t, registrar, 100, "INVITE")

	var incoming *IncomingInvite
	select {
	case incoming = <-client.IncomingCalls():
	case <-time.After(time.Second):
		t.Fatal("incoming INVITE was not delivered")
	}
	call := incoming.Call()
	if call.Codec.Name != "pcma" || call.Codec.PayloadType != 8 || call.RemoteRTPAddr().Port != 40000 {
		t.Fatalf("unexpected incoming media negotiation: %#v %v", call.Codec, call.RemoteRTPAddr())
	}
	if call.TelephoneEvent == nil || call.TelephoneEvent.PayloadType != 101 || call.TelephoneEvent.ClockRate != 8000 {
		t.Fatalf("unexpected incoming DTMF negotiation: %#v", call.TelephoneEvent)
	}
	if incoming.CallerURI() != "sip:**610@127.0.0.1" {
		t.Fatalf("unexpected caller URI %q", incoming.CallerURI())
	}
	if incoming.CallerID() != "**610" || !call.IsInbound() {
		t.Fatalf("unexpected normalized caller/direction: %q inbound=%t", incoming.CallerID(), call.IsInbound())
	}

	answerPort := freeUDPPort(t)
	if err := incoming.Answer(answerPort); err != nil {
		t.Fatal(err)
	}
	answer := readSIPResponse(t, registrar, 200, "INVITE")
	if !strings.Contains(string(answer.Body), fmt.Sprintf("m=audio %d RTP/AVP 8 101", answerPort)) ||
		!strings.Contains(string(answer.Body), "a=rtpmap:8 PCMA/8000") ||
		!strings.Contains(string(answer.Body), "a=rtpmap:101 telephone-event/8000") {
		t.Fatalf("unexpected SDP answer: %s", answer.Body)
	}
	if param(answer.Header("to"), "tag") == "" {
		t.Fatal("successful INVITE answer has no To tag")
	}

	ack := buildMockDialogRequest("ACK", 1, inviteReq, answer, clientAddr, registrar.LocalAddr().(*net.UDPAddr))
	if _, err := registrar.WriteToUDP(ack, clientAddr); err != nil {
		t.Fatal(err)
	}
	bye := buildMockDialogRequest("BYE", 2, inviteReq, answer, clientAddr, registrar.LocalAddr().(*net.UDPAddr))
	if _, err := registrar.WriteToUDP(bye, clientAddr); err != nil {
		t.Fatal(err)
	}
	_ = readSIPResponse(t, registrar, 200, "BYE")
	select {
	case err := <-call.Done():
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote BYE did not finish incoming call")
	}
}

func TestIncomingInviteCanBeCanceledBeforeAnswer(t *testing.T) {
	registrar, client, clientAddr := newIncomingTestClient(t, true)
	sdp := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 40000 RTP/AVP 8\r\na=rtpmap:8 PCMA/8000\r\n"
	packet, req := buildMockIncomingInvite(t, clientAddr, registrar.LocalAddr().(*net.UDPAddr), "incoming-cancel", "z9hG4bK-incoming-cancel", sdp)
	if _, err := registrar.WriteToUDP(packet, clientAddr); err != nil {
		t.Fatal(err)
	}
	_ = readSIPResponse(t, registrar, 100, "INVITE")
	var incoming *IncomingInvite
	select {
	case incoming = <-client.IncomingCalls():
	case <-time.After(time.Second):
		t.Fatal("incoming INVITE was not delivered")
	}

	cancel := buildMockCancel(req, clientAddr, registrar.LocalAddr().(*net.UDPAddr))
	if _, err := registrar.WriteToUDP(cancel, clientAddr); err != nil {
		t.Fatal(err)
	}
	gotCancelOK, gotInvite487 := false, false
	deadline := time.Now().Add(2 * time.Second)
	for !gotCancelOK || !gotInvite487 {
		response := readAnySIPMessage(t, registrar, deadline)
		if response.StatusCode == 200 && cseqMethod(response.Header("cseq")) == "CANCEL" {
			gotCancelOK = true
		}
		if response.StatusCode == 487 && cseqMethod(response.Header("cseq")) == "INVITE" {
			gotInvite487 = true
		}
	}
	select {
	case err := <-incoming.Call().Done():
		if !errors.Is(err, ErrIncomingCallCanceled) {
			t.Fatalf("unexpected cancellation result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CANCEL did not finish incoming call")
	}
	if err := incoming.Answer(freeUDPPort(t)); !errors.Is(err, ErrIncomingCallCanceled) {
		t.Fatalf("answer after CANCEL returned %v", err)
	}
}

func TestIncomingInviteIsDeduplicatedAndAnswerRetransmitsUntilACK(t *testing.T) {
	registrar, client, clientAddr := newIncomingTestClient(t, true)
	sdp := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 40000 RTP/AVP 8\r\n"
	packet, req := buildMockIncomingInvite(t, clientAddr, registrar.LocalAddr().(*net.UDPAddr), "incoming-retry", "z9hG4bK-incoming-retry", sdp)
	_, _ = registrar.WriteToUDP(packet, clientAddr)
	_ = readSIPResponse(t, registrar, 100, "INVITE")
	var incoming *IncomingInvite
	select {
	case incoming = <-client.IncomingCalls():
	case <-time.After(time.Second):
		t.Fatal("incoming INVITE was not delivered")
	}

	// UDP may repeat an INVITE. The same server transaction must repeat its
	// response without creating a second application call.
	_, _ = registrar.WriteToUDP(packet, clientAddr)
	_ = readSIPResponse(t, registrar, 100, "INVITE")
	select {
	case <-client.IncomingCalls():
		t.Fatal("retransmitted INVITE created a duplicate call")
	case <-time.After(75 * time.Millisecond):
	}

	if err := incoming.Answer(freeUDPPort(t)); err != nil {
		t.Fatal(err)
	}
	answer := readSIPResponse(t, registrar, 200, "INVITE")
	// Do not ACK the first 200. The UAS must retransmit it over UDP.
	_ = readSIPResponse(t, registrar, 200, "INVITE")
	ack := buildMockDialogRequest("ACK", 1, req, answer, clientAddr, registrar.LocalAddr().(*net.UDPAddr))
	_, _ = registrar.WriteToUDP(ack, clientAddr)
	bye := buildMockDialogRequest("BYE", 2, req, answer, clientAddr, registrar.LocalAddr().(*net.UDPAddr))
	_, _ = registrar.WriteToUDP(bye, clientAddr)
	_ = readSIPResponse(t, registrar, 200, "BYE")
	select {
	case <-incoming.Call().Done():
	case <-time.After(time.Second):
		t.Fatal("incoming call did not finish")
	}
}

func TestIncomingCallCanHangUpLocally(t *testing.T) {
	registrar, client, clientAddr := newIncomingTestClient(t, true)
	sdp := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 40000 RTP/AVP 8\r\n"
	packet, req := buildMockIncomingInvite(t, clientAddr, registrar.LocalAddr().(*net.UDPAddr), "incoming-local-bye", "z9hG4bK-incoming-local-bye", sdp)
	_, _ = registrar.WriteToUDP(packet, clientAddr)
	_ = readSIPResponse(t, registrar, 100, "INVITE")
	var incoming *IncomingInvite
	select {
	case incoming = <-client.IncomingCalls():
	case <-time.After(time.Second):
		t.Fatal("incoming INVITE was not delivered")
	}
	if err := incoming.Answer(freeUDPPort(t)); err != nil {
		t.Fatal(err)
	}
	answer := readSIPResponse(t, registrar, 200, "INVITE")
	ack := buildMockDialogRequest("ACK", 1, req, answer, clientAddr, registrar.LocalAddr().(*net.UDPAddr))
	_, _ = registrar.WriteToUDP(ack, clientAddr)

	serverErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 65535)
		_ = registrar.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, addr, err := registrar.ReadFromUDP(buf)
		if err != nil {
			serverErr <- err
			return
		}
		bye, err := parseMessage(buf[:n])
		if err != nil {
			serverErr <- err
			return
		}
		if bye.Method != "BYE" || param(bye.Header("from"), "tag") != param(answer.Header("to"), "tag") || param(bye.Header("to"), "tag") != "phone-tag" {
			serverErr <- fmt.Errorf("invalid inbound-dialog BYE: from=%q to=%q", bye.Header("from"), bye.Header("to"))
			return
		}
		serverErr <- mockResponse(registrar, addr, bye, 200, "OK", nil, nil)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := incoming.Call().Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestIncomingCallsDisabledAndBusyAreRejected(t *testing.T) {
	sdp := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 40000 RTP/AVP 8\r\n"
	t.Run("disabled", func(t *testing.T) {
		registrar, _, clientAddr := newIncomingTestClient(t, false)
		packet, _ := buildMockIncomingInvite(t, clientAddr, registrar.LocalAddr().(*net.UDPAddr), "disabled", "z9hG4bK-disabled", sdp)
		_, _ = registrar.WriteToUDP(packet, clientAddr)
		_ = readSIPResponse(t, registrar, 403, "INVITE")
	})
	t.Run("busy", func(t *testing.T) {
		registrar, client, clientAddr := newIncomingTestClient(t, true)
		serverAddr := registrar.LocalAddr().(*net.UDPAddr)
		first, _ := buildMockIncomingInvite(t, clientAddr, serverAddr, "busy-1", "z9hG4bK-busy-1", sdp)
		_, _ = registrar.WriteToUDP(first, clientAddr)
		_ = readSIPResponse(t, registrar, 100, "INVITE")
		var pending *IncomingInvite
		select {
		case pending = <-client.IncomingCalls():
		case <-time.After(time.Second):
			t.Fatal("first incoming call missing")
		}
		second, _ := buildMockIncomingInvite(t, clientAddr, serverAddr, "busy-2", "z9hG4bK-busy-2", sdp)
		_, _ = registrar.WriteToUDP(second, clientAddr)
		_ = readSIPResponse(t, registrar, 486, "INVITE")
		if err := pending.Reject(486, "Busy Here"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestIncomingInviteRequiresConfiguredRegistrarSource(t *testing.T) {
	registrar, _, clientAddr := newIncomingTestClient(t, true)
	attacker, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer attacker.Close()
	sdp := "v=0\r\nc=IN IP4 127.0.0.1\r\nm=audio 40000 RTP/AVP 8\r\n"
	packet, _ := buildMockIncomingInvite(t, clientAddr, attacker.LocalAddr().(*net.UDPAddr), "untrusted", "z9hG4bK-untrusted", sdp)
	if _, err := attacker.WriteToUDP(packet, clientAddr); err != nil {
		t.Fatal(err)
	}
	_ = readSIPResponse(t, attacker, 403, "INVITE")
	_ = registrar
}

func TestIncomingInviteRejectsCallerOutsideWhitelist(t *testing.T) {
	registrar, client, clientAddr := newIncomingTestClientWithCallers(t, true, []string{"0123 456789"})
	// The unsupported body would produce 488 if SDP were inspected first. A
	// 403 therefore also proves that authorization precedes media parsing.
	sdp := "v=0\r\n"
	packet, _ := buildMockIncomingInvite(t, clientAddr, registrar.LocalAddr().(*net.UDPAddr), "caller-denied", "z9hG4bK-caller-denied", sdp)
	if _, err := registrar.WriteToUDP(packet, clientAddr); err != nil {
		t.Fatal(err)
	}
	_ = readSIPResponse(t, registrar, 403, "INVITE")
	select {
	case invite := <-client.IncomingCalls():
		t.Fatalf("rejected caller reached application: %#v", invite)
	case <-time.After(100 * time.Millisecond):
	}
}

func newIncomingTestClient(t *testing.T, enabled bool) (*net.UDPConn, *Client, *net.UDPAddr) {
	t.Helper()
	return newIncomingTestClientWithCallers(t, enabled, []string{"*"})
}

func newIncomingTestClientWithCallers(t *testing.T, enabled bool, callers []string) (*net.UDPConn, *Client, *net.UDPAddr) {
	t.Helper()
	registrar, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	localPort := freeUDPPort(t)
	registrarAddr := registrar.LocalAddr().(*net.UDPAddr)
	client, err := New(Config{
		Registrar: "127.0.0.1", RegistrarPort: registrarAddr.Port, Username: "621", Password: "secret",
		LocalPort: localPort, DisplayName: "Door", CodecPreference: "pcma", AcceptIncoming: enabled, AllowedCallers: callers,
	}, nil)
	if err != nil {
		registrar.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		registrar.Close()
	})
	return registrar, client, &net.UDPAddr{IP: client.LocalIP(), Port: localPort}
}

func buildMockIncomingInvite(t *testing.T, clientAddr, registrarAddr *net.UDPAddr, callID, branch, sdp string) ([]byte, Message) {
	t.Helper()
	lines := []string{
		fmt.Sprintf("INVITE sip:621@%s SIP/2.0", clientAddr.String()),
		fmt.Sprintf("Via: SIP/2.0/UDP %s;branch=%s;rport", registrarAddr.String(), branch),
		"Max-Forwards: 70",
		"From: \"Phone\" <sip:**610@127.0.0.1>;tag=phone-tag",
		"To: <sip:621@127.0.0.1>",
		"Call-ID: " + callID,
		"CSeq: 1 INVITE",
		fmt.Sprintf("Contact: <sip:phone@%s>", registrarAddr.String()),
		"Content-Type: application/sdp",
		fmt.Sprintf("Content-Length: %d", len(sdp)), "", "",
	}
	packet := append([]byte(strings.Join(lines, "\r\n")), []byte(sdp)...)
	req, err := parseMessage(packet)
	if err != nil {
		t.Fatal(err)
	}
	return packet, req
}

func buildMockCancel(invite Message, clientAddr, registrarAddr *net.UDPAddr) []byte {
	lines := []string{
		fmt.Sprintf("CANCEL sip:621@%s SIP/2.0", clientAddr.String()),
		"Via: " + invite.Header("via"),
		"Max-Forwards: 70", "From: " + invite.Header("from"), "To: " + invite.Header("to"),
		"Call-ID: " + invite.Header("call-id"), "CSeq: 1 CANCEL", "Content-Length: 0", "", "",
	}
	_ = registrarAddr
	return []byte(strings.Join(lines, "\r\n"))
}

func buildMockDialogRequest(method string, cseq int, invite, answer Message, clientAddr, registrarAddr *net.UDPAddr) []byte {
	lines := []string{
		fmt.Sprintf("%s sip:621@%s SIP/2.0", method, clientAddr.String()),
		fmt.Sprintf("Via: SIP/2.0/UDP %s;branch=%s", registrarAddr.String(), branchID()),
		"Max-Forwards: 70", "From: " + invite.Header("from"), "To: " + answer.Header("to"),
		"Call-ID: " + invite.Header("call-id"), fmt.Sprintf("CSeq: %d %s", cseq, method), "Content-Length: 0", "", "",
	}
	return []byte(strings.Join(lines, "\r\n"))
}

func readSIPResponse(t *testing.T, conn *net.UDPConn, status int, method string) Message {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		message := readAnySIPMessage(t, conn, deadline)
		if message.IsResponse && message.StatusCode == status && cseqMethod(message.Header("cseq")) == method {
			return message
		}
	}
}

func readAnySIPMessage(t *testing.T, conn *net.UDPConn, deadline time.Time) Message {
	t.Helper()
	buf := make([]byte, 65535)
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	message, err := parseMessage(buf[:n])
	if err != nil {
		t.Fatal(err)
	}
	return message
}

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
				if !strings.Contains(string(m.Body), "a=rtpmap:101 telephone-event/8000") {
					serverErr <- fmt.Errorf("outgoing INVITE did not offer RFC 4733 DTMF")
					return
				}
				if m.Header("authorization") == "" {
					_ = mockResponse(server, addr, m, 401, "Unauthorized", []string{`WWW-Authenticate: Digest realm="fritz.box", nonce="def", qop="auth"`}, nil)
				} else {
					inviteAuthenticated = true
					_ = mockResponse(server, addr, m, 180, "Ringing", nil, nil)
					sdp := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=mock\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 8 101\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\n"
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
	if call.TelephoneEvent == nil || call.TelephoneEvent.PayloadType != 101 || call.TelephoneEvent.ClockRate != 8000 {
		t.Fatalf("bad DTMF negotiation: %#v", call.TelephoneEvent)
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
