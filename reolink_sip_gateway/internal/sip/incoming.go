package sip

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrIncomingCallCanceled = errors.New("incoming SIP call canceled")
	ErrIncomingACKTimeout   = errors.New("incoming SIP call was not acknowledged")
)

type incomingState uint8

const (
	incomingPending incomingState = iota
	incomingAnswered
	incomingAcknowledged
	incomingRejected
	incomingCanceled
)

// IncomingInvite represents one incoming SIP INVITE after source and SDP
// validation. The application may prepare the camera media path before calling
// Answer, so a caller is never connected to a known-silent session.
type IncomingInvite struct {
	client  *Client
	request Message
	source  *net.UDPAddr
	key     string
	call    *Call

	mu           sync.Mutex
	state        incomingState
	lastResponse []byte
	acked        chan struct{}
	ackOnce      sync.Once
	stop         chan struct{}
	stopOnce     sync.Once
	expiryOnce   sync.Once
}

func (i *IncomingInvite) Call() *Call { return i.call }

func (i *IncomingInvite) CallerURI() string {
	if i == nil || i.call == nil {
		return ""
	}
	return i.call.ToURI
}

// Answer completes the INVITE with one negotiated G.711 codec and the RTP
// port that the application reserved before camera setup.
func (i *IncomingInvite) Answer(rtpPort int) error {
	if rtpPort < 1 || rtpPort > 65535 {
		return fmt.Errorf("invalid RTP port %d", rtpPort)
	}
	sdp := i.client.answerSDP(rtpPort, i.call.Codec)
	extra := []string{
		fmt.Sprintf("Contact: <sip:%s@%s:%d;transport=udp>", i.client.cfg.Username, i.client.localIP, i.client.cfg.LocalPort),
		"Allow: INVITE, ACK, CANCEL, BYE, OPTIONS",
		"Content-Type: application/sdp",
	}
	packet := buildResponse(i.request, 200, "OK", i.call.FromTag, extra, []byte(sdp))

	i.mu.Lock()
	if i.state != incomingPending {
		state := i.state
		i.mu.Unlock()
		if state == incomingCanceled {
			return ErrIncomingCallCanceled
		}
		return errors.New("incoming SIP call is no longer pending")
	}
	i.state = incomingAnswered
	i.lastResponse = packet
	i.mu.Unlock()

	if _, err := i.client.conn.WriteToUDP(packet, i.source); err != nil {
		i.call.finish(fmt.Errorf("send incoming SIP answer: %w", err))
		return err
	}
	if i.client.log != nil {
		i.client.log.Info("incoming SIP call answered", "caller", i.CallerURI(), "codec", i.call.Codec.Name, "rtp", i.call.RemoteRTPAddr())
	}
	go i.retransmitAnswer()
	return nil
}

// Reject terminates a still-pending INVITE. It is used when the application is
// busy or cannot prepare the configured Reolink media path.
func (i *IncomingInvite) Reject(code int, reason string) error {
	if code < 400 || code > 699 {
		return fmt.Errorf("invalid SIP rejection status %d", code)
	}
	packet := buildResponse(i.request, code, reason, i.call.FromTag, []string{"Allow: INVITE, ACK, CANCEL, BYE, OPTIONS"}, nil)
	i.mu.Lock()
	if i.state != incomingPending {
		state := i.state
		i.mu.Unlock()
		if state == incomingCanceled {
			return ErrIncomingCallCanceled
		}
		return errors.New("incoming SIP call is no longer pending")
	}
	i.state = incomingRejected
	i.lastResponse = packet
	i.mu.Unlock()

	_, err := i.client.conn.WriteToUDP(packet, i.source)
	i.call.finish(fmt.Errorf("incoming SIP call rejected with %d %s", code, reason))
	i.expireAfter(32 * time.Second)
	return err
}

func (i *IncomingInvite) resendLastResponse(addr *net.UDPAddr) {
	i.mu.Lock()
	packet := append([]byte(nil), i.lastResponse...)
	i.mu.Unlock()
	if len(packet) > 0 {
		_, _ = i.client.conn.WriteToUDP(packet, addr)
	}
}

func (i *IncomingInvite) acknowledge(req Message) {
	if cseqNumber(req.Header("cseq")) != cseqNumber(i.request.Header("cseq")) {
		return
	}
	i.mu.Lock()
	acknowledged := false
	if i.state == incomingAnswered {
		i.state = incomingAcknowledged
		i.ackOnce.Do(func() { close(i.acked) })
		acknowledged = true
	}
	i.mu.Unlock()
	if acknowledged {
		i.expireAfter(32 * time.Second)
	}
}

func (i *IncomingInvite) cancel(cancelReq Message, addr *net.UDPAddr) {
	i.mu.Lock()
	pending := i.state == incomingPending
	if pending {
		i.state = incomingCanceled
		i.lastResponse = buildResponse(i.request, 487, "Request Terminated", i.call.FromTag, nil, nil)
	}
	inviteFinal := append([]byte(nil), i.lastResponse...)
	i.mu.Unlock()

	if !pending {
		packet := buildResponse(cancelReq, 481, "Call/Transaction Does Not Exist", i.call.FromTag, nil, nil)
		_, _ = i.client.conn.WriteToUDP(packet, addr)
		return
	}
	cancelOK := buildResponse(cancelReq, 200, "OK", i.call.FromTag, nil, nil)
	_, _ = i.client.conn.WriteToUDP(cancelOK, addr)
	_, _ = i.client.conn.WriteToUDP(inviteFinal, i.source)
	i.call.finish(ErrIncomingCallCanceled)
	i.expireAfter(32 * time.Second)
}

func (i *IncomingInvite) retransmitAnswer() {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	deadline := time.NewTimer(32 * time.Second)
	defer deadline.Stop()
	interval := 500 * time.Millisecond
	for {
		select {
		case <-i.acked:
			return
		case <-i.stop:
			return
		case <-deadline.C:
			i.call.finish(ErrIncomingACKTimeout)
			return
		case <-timer.C:
			i.resendLastResponse(i.source)
			if interval < 4*time.Second {
				interval *= 2
			}
			timer.Reset(interval)
		}
	}
}

func (i *IncomingInvite) stopRetransmission() {
	i.stopOnce.Do(func() { close(i.stop) })
	i.expireAfter(32 * time.Second)
}

func (i *IncomingInvite) expireAfter(delay time.Duration) {
	i.expiryOnce.Do(func() {
		go func() {
			t := time.NewTimer(delay)
			defer t.Stop()
			select {
			case <-i.client.closed:
			case <-t.C:
			}
			i.client.mu.Lock()
			if i.client.serverInvites[i.key] == i {
				delete(i.client.serverInvites, i.key)
			}
			i.client.mu.Unlock()
		}()
	})
}

func (c *Client) handleIncomingInvite(req Message, addr *net.UDPAddr) {
	if !c.cfg.AcceptIncoming {
		_ = c.sendTaggedResponse(req, addr, 403, "Forbidden")
		return
	}
	if !c.isRegistrarSource(addr) {
		if c.log != nil {
			c.log.Warn("incoming SIP INVITE rejected from untrusted source", "source", addr.String(), "registrar", c.registrar.String())
		}
		_ = c.sendTaggedResponse(req, addr, 403, "Forbidden")
		return
	}
	if err := validateIncomingInviteHeaders(req); err != nil {
		if c.log != nil {
			c.log.Warn("malformed incoming SIP INVITE rejected", "source", addr.String(), "error", err)
		}
		_ = c.sendTaggedResponse(req, addr, 400, "Bad Request")
		return
	}

	key := serverInviteKey(req)
	c.mu.Lock()
	if prior := c.serverInvites[key]; prior != nil {
		c.mu.Unlock()
		prior.resendLastResponse(addr)
		return
	}
	if c.active != nil || c.dialing {
		c.mu.Unlock()
		_ = c.sendTaggedResponse(req, addr, 486, "Busy Here")
		return
	}
	c.mu.Unlock()

	codec, remoteRTP, err := parseOfferSDP(string(req.Body), c.cfg.CodecPreference)
	if err != nil {
		if c.log != nil {
			c.log.Warn("incoming SIP INVITE has no supported audio offer", "source", addr.String(), "error", err)
		}
		_ = c.sendTaggedResponse(req, addr, 488, "Not Acceptable Here")
		return
	}

	localURI := extractURI(req.Header("to"))
	remoteURI := extractURI(req.Header("from"))
	remoteTarget := extractURI(req.Header("contact"))
	if remoteTarget == "" {
		remoteTarget = fmt.Sprintf("sip:remote@%s", addr.String())
	}
	call := &Call{
		client: c, CallID: req.Header("call-id"), FromTag: randomID(), ToTag: param(req.Header("from"), "tag"),
		FromURI: localURI, ToURI: remoteURI, RemoteTarget: remoteTarget, Codec: codec,
		RemoteRTP: remoteRTP, done: make(chan error, 1), inbound: true,
	}
	invite := &IncomingInvite{
		client: c, request: req, source: cloneUDPAddr(addr), key: key, call: call,
		state: incomingPending, acked: make(chan struct{}), stop: make(chan struct{}),
	}
	call.incoming = invite
	trying := buildResponse(req, 100, "Trying", "", nil, nil)
	invite.lastResponse = trying

	c.mu.Lock()
	// Re-check after SDP parsing so an outbound call cannot claim the client in
	// the small window between the first busy check and dialog reservation.
	if c.active != nil || c.dialing {
		c.mu.Unlock()
		_ = c.sendTaggedResponse(req, addr, 486, "Busy Here")
		return
	}
	if prior := c.serverInvites[key]; prior != nil {
		c.mu.Unlock()
		prior.resendLastResponse(addr)
		return
	}
	c.active = call
	c.serverInvites[key] = invite
	c.mu.Unlock()

	_, _ = c.conn.WriteToUDP(trying, addr)
	select {
	case c.incoming <- invite:
		if c.log != nil {
			c.log.Info("incoming SIP call received", "caller", invite.CallerURI(), "codec", codec.Name, "rtp", remoteRTP.String())
		}
	case <-c.closed:
		call.finish(errors.New("SIP client closed"))
	default:
		_ = invite.Reject(503, "Service Unavailable")
	}
}

func (c *Client) handleIncomingACK(req Message, addr *net.UDPAddr) {
	if !c.isRegistrarSource(addr) {
		return
	}
	c.mu.Lock()
	call := c.active
	c.mu.Unlock()
	if call != nil && call.inbound && req.Header("call-id") == call.CallID && call.incoming != nil {
		call.incoming.acknowledge(req)
	}
}

func (c *Client) handleIncomingCancel(req Message, addr *net.UDPAddr) {
	if !c.isRegistrarSource(addr) {
		_ = c.sendTaggedResponse(req, addr, 403, "Forbidden")
		return
	}
	key := serverInviteKey(req)
	c.mu.Lock()
	invite := c.serverInvites[key]
	c.mu.Unlock()
	if invite == nil {
		_ = c.sendTaggedResponse(req, addr, 481, "Call/Transaction Does Not Exist")
		return
	}
	invite.cancel(req, addr)
}

func (c *Client) isRegistrarSource(addr *net.UDPAddr) bool {
	return addr != nil && c.registrar != nil && addr.Port == c.registrar.Port && addr.IP.Equal(c.registrar.IP)
}

func (c *Client) sendTaggedResponse(req Message, addr *net.UDPAddr, code int, reason string) error {
	packet := buildResponse(req, code, reason, statelessResponseTag(req), []string{"Allow: INVITE, ACK, CANCEL, BYE, OPTIONS"}, nil)
	_, err := c.conn.WriteToUDP(packet, addr)
	return err
}

func statelessResponseTag(req Message) string {
	sum := sha256.Sum256([]byte(serverInviteKey(req)))
	return fmt.Sprintf("%x", sum[:8])
}

func serverInviteKey(req Message) string {
	return strings.Join([]string{req.Header("call-id"), strconv.FormatUint(uint64(cseqNumber(req.Header("cseq"))), 10), param(req.Header("via"), "branch")}, "|")
}

func validateIncomingInviteHeaders(req Message) error {
	for _, name := range []string{"via", "from", "to", "call-id", "cseq"} {
		if strings.TrimSpace(req.Header(name)) == "" {
			return fmt.Errorf("missing %s header", name)
		}
	}
	if param(req.Header("via"), "branch") == "" {
		return errors.New("top Via has no branch")
	}
	if param(req.Header("from"), "tag") == "" {
		return errors.New("From has no tag")
	}
	if cseqNumber(req.Header("cseq")) == 0 || cseqMethod(req.Header("cseq")) != "INVITE" {
		return errors.New("invalid INVITE CSeq")
	}
	return nil
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}

func buildResponse(req Message, code int, reason, toTag string, extra []string, body []byte) []byte {
	lines := []string{fmt.Sprintf("SIP/2.0 %d %s", code, reason)}
	for _, v := range req.Headers["via"] {
		lines = append(lines, "Via: "+v)
	}
	lines = append(lines, "From: "+req.Header("from"))
	to := req.Header("to")
	if toTag != "" && param(to, "tag") == "" {
		to += ";tag=" + toTag
	}
	lines = append(lines, "To: "+to, "Call-ID: "+req.Header("call-id"), "CSeq: "+req.Header("cseq"))
	lines = append(lines, extra...)
	lines = append(lines, "Server: ReolinkSIPGateway/0.7.0", fmt.Sprintf("Content-Length: %d", len(body)), "", "")
	return append([]byte(strings.Join(lines, "\r\n")), body...)
}

func (c *Client) answerSDP(port int, codec Codec) string {
	name := strings.ToUpper(codec.Name)
	return strings.Join([]string{
		"v=0",
		fmt.Sprintf("o=- %d 1 IN IP4 %s", time.Now().Unix(), c.localIP),
		"s=Reolink SIP Gateway",
		fmt.Sprintf("c=IN IP4 %s", c.localIP),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %d", port, codec.PayloadType),
		fmt.Sprintf("a=rtpmap:%d %s/8000", codec.PayloadType, name),
		"a=ptime:20",
		"a=sendrecv",
		"",
	}, "\r\n")
}
