package sip

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/digest"
)

type Config struct {
	Registrar       string
	RegistrarPort   int
	Username        string
	Password        string
	LocalPort       int
	DisplayName     string
	CodecPreference string
	AcceptIncoming  bool
	AllowedCallers  []string
	Debug           bool
}

type Client struct {
	cfg       Config
	log       *slog.Logger
	conn      *net.UDPConn
	registrar *net.UDPAddr
	localIP   net.IP

	mu              sync.Mutex
	transactions    map[string]chan Message
	active          *Call
	dialing         bool
	incoming        chan *IncomingInvite
	serverInvites   map[string]*IncomingInvite
	allowedCallers  callerAllowlist
	registerCallID  string
	registerCSeq    uint32
	registered      atomic.Bool
	lastRegisterErr atomic.Value
	closed          chan struct{}
	closeOnce       sync.Once
}

type Message struct {
	IsResponse bool
	StatusCode int
	Reason     string
	Method     string
	URI        string
	Headers    map[string][]string
	Body       []byte
	Raw        string
}

type Codec struct {
	Name        string
	PayloadType uint8
}

type Call struct {
	client       *Client
	CallID       string
	FromTag      string
	ToTag        string
	FromURI      string
	ToURI        string
	RemoteTarget string
	CallerID     string
	CSeq         uint32
	Codec        Codec
	RemoteRTP    *net.UDPAddr
	done         chan error
	doneOnce     sync.Once
	rtpMu        sync.RWMutex
	inbound      bool
	incoming     *IncomingInvite
}

func New(cfg Config, logger *slog.Logger) (*Client, error) {
	reg, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(cfg.Registrar, strconv.Itoa(cfg.RegistrarPort)))
	if err != nil {
		return nil, err
	}
	dial, err := net.DialUDP("udp4", nil, reg)
	if err != nil {
		return nil, fmt.Errorf("determine local IP: %w", err)
	}
	localIP := dial.LocalAddr().(*net.UDPAddr).IP
	_ = dial.Close()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: localIP, Port: cfg.LocalPort})
	if err != nil {
		return nil, fmt.Errorf("listen SIP UDP: %w", err)
	}
	c := &Client{cfg: cfg, log: logger, conn: conn, registrar: reg, localIP: localIP,
		transactions: make(map[string]chan Message), incoming: make(chan *IncomingInvite, 4),
		serverInvites: make(map[string]*IncomingInvite), allowedCallers: newCallerAllowlist(cfg.AllowedCallers),
		registerCallID: randomID() + "@" + localIP.String(), closed: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *Client) LocalIP() net.IP                       { return append(net.IP(nil), c.localIP...) }
func (c *Client) Registered() bool                      { return c.registered.Load() }
func (c *Client) IncomingCalls() <-chan *IncomingInvite { return c.incoming }
func (c *Client) LastRegisterError() string {
	v := c.lastRegisterErr.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

func (c *Client) StartRegistration(ctx context.Context) {
	go func() {
		backoff := time.Second
		requestedExpires := 300
		for {
			grantedExpires, err := c.register(ctx, requestedExpires)
			if err != nil {
				c.registered.Store(false)
				c.lastRegisterErr.Store(err.Error())
				if c.log != nil {
					c.log.Error("SIP registration failed", "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 30*time.Second {
					backoff *= 2
					if backoff > 30*time.Second {
						backoff = 30 * time.Second
					}
				}
				continue
			}
			c.registered.Store(true)
			c.lastRegisterErr.Store("")
			backoff = time.Second
			if grantedExpires <= 0 {
				grantedExpires = requestedExpires
			}
			refreshAfter := time.Duration(grantedExpires) * time.Second * 4 / 5
			if refreshAfter < time.Second {
				refreshAfter = time.Second
			}
			if c.log != nil {
				c.log.Info("SIP registration active", "registrar", c.registrar.String(), "user", c.cfg.Username, "expires", grantedExpires, "refresh_in", refreshAfter)
			}
			t := time.NewTimer(refreshAfter)
			select {
			case <-ctx.Done():
				if !t.Stop() {
					<-t.C
				}
				unregisterCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, _ = c.register(unregisterCtx, 0)
				cancel()
				c.registered.Store(false)
				return
			case <-t.C:
			}
		}
	}()
}

func (c *Client) register(ctx context.Context, expires int) (int, error) {
	c.mu.Lock()
	c.registerCSeq++
	cseq := c.registerCSeq
	c.mu.Unlock()
	uri := fmt.Sprintf("sip:%s:%d", c.cfg.Registrar, c.cfg.RegistrarPort)
	fromURI := fmt.Sprintf("sip:%s@%s", c.cfg.Username, c.cfg.Registrar)
	tag := randomID()
	branch := branchID()
	headers := c.baseHeaders("REGISTER", uri, branch, cseq, c.registerCallID, fromURI, fromURI, tag, "")
	headers = append(headers,
		fmt.Sprintf("Contact: <sip:%s@%s:%d;transport=udp>;expires=%d", c.cfg.Username, c.localIP, c.cfg.LocalPort, expires),
		fmt.Sprintf("Expires: %d", expires),
	)
	res, err := c.transact(ctx, "REGISTER", branch, cseq, uri, headers, nil, false)
	if err != nil {
		return 0, err
	}
	if res.StatusCode == 401 || res.StatusCode == 407 {
		name := "www-authenticate"
		authName := "Authorization"
		if res.StatusCode == 407 {
			name = "proxy-authenticate"
			authName = "Proxy-Authorization"
		}
		challenge, err := digest.Parse(res.Header(name))
		if err != nil {
			return 0, err
		}
		c.mu.Lock()
		c.registerCSeq++
		cseq = c.registerCSeq
		c.mu.Unlock()
		branch = branchID()
		headers = c.baseHeaders("REGISTER", uri, branch, cseq, c.registerCallID, fromURI, fromURI, tag, "")
		headers = append(headers,
			fmt.Sprintf("Contact: <sip:%s@%s:%d;transport=udp>;expires=%d", c.cfg.Username, c.localIP, c.cfg.LocalPort, expires),
			fmt.Sprintf("Expires: %d", expires),
			fmt.Sprintf("%s: %s", authName, digest.Authorization(challenge, c.cfg.Username, c.cfg.Password, "REGISTER", uri, 1)),
		)
		res, err = c.transact(ctx, "REGISTER", branch, cseq, uri, headers, nil, false)
		if err != nil {
			return 0, err
		}
	}
	if res.StatusCode == 423 && expires > 0 {
		if minExpires, err := strconv.Atoi(strings.TrimSpace(res.Header("min-expires"))); err == nil && minExpires > expires {
			return c.register(ctx, minExpires)
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return 0, fmt.Errorf("REGISTER returned %d %s", res.StatusCode, res.Reason)
	}
	granted := expires
	if v, err := strconv.Atoi(strings.TrimSpace(res.Header("expires"))); err == nil && v >= 0 {
		granted = v
	} else if v, err := strconv.Atoi(param(res.Header("contact"), "expires")); err == nil && v >= 0 {
		granted = v
	}
	return granted, nil
}

func (c *Client) Dial(ctx context.Context, destination string, rtpPort int) (*Call, error) {
	c.mu.Lock()
	if c.active != nil || c.dialing {
		c.mu.Unlock()
		return nil, errors.New("another SIP call is active")
	}
	c.dialing = true
	c.mu.Unlock()
	established := false
	defer func() {
		if established {
			return
		}
		c.mu.Lock()
		c.dialing = false
		c.mu.Unlock()
	}()
	toURI := destination
	if !strings.HasPrefix(strings.ToLower(toURI), "sip:") {
		toURI = fmt.Sprintf("sip:%s@%s", destination, c.cfg.Registrar)
	}
	fromURI := fmt.Sprintf("sip:%s@%s", c.cfg.Username, c.cfg.Registrar)
	callID := randomID() + "@" + c.localIP.String()
	fromTag := randomID()
	cseq := uint32(1)
	branch := branchID()
	sdp := c.offerSDP(rtpPort)
	headers := c.baseHeaders("INVITE", toURI, branch, cseq, callID, fromURI, toURI, fromTag, "")
	headers = append(headers, "Content-Type: application/sdp", "Allow: INVITE, ACK, CANCEL, BYE, OPTIONS")
	res, err := c.transact(ctx, "INVITE", branch, cseq, toURI, headers, []byte(sdp), true)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == 401 || res.StatusCode == 407 {
		_ = c.sendInviteFinalACK(toURI, branch, cseq, callID, fromURI, toURI, fromTag, param(res.Header("to"), "tag"))
		name := "www-authenticate"
		authName := "Authorization"
		if res.StatusCode == 407 {
			name = "proxy-authenticate"
			authName = "Proxy-Authorization"
		}
		challenge, err := digest.Parse(res.Header(name))
		if err != nil {
			return nil, err
		}
		cseq++
		branch = branchID()
		headers = c.baseHeaders("INVITE", toURI, branch, cseq, callID, fromURI, toURI, fromTag, "")
		headers = append(headers, "Content-Type: application/sdp", "Allow: INVITE, ACK, CANCEL, BYE, OPTIONS",
			fmt.Sprintf("%s: %s", authName, digest.Authorization(challenge, c.cfg.Username, c.cfg.Password, "INVITE", toURI, 1)))
		res, err = c.transact(ctx, "INVITE", branch, cseq, toURI, headers, []byte(sdp), true)
		if err != nil {
			return nil, err
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_ = c.sendInviteFinalACK(toURI, branch, cseq, callID, fromURI, toURI, fromTag, param(res.Header("to"), "tag"))
		return nil, fmt.Errorf("INVITE returned %d %s", res.StatusCode, res.Reason)
	}
	toTag := param(res.Header("to"), "tag")
	remoteTarget := extractURI(res.Header("contact"))
	if remoteTarget == "" {
		remoteTarget = toURI
	}
	call := &Call{client: c, CallID: callID, FromTag: fromTag, ToTag: toTag, FromURI: fromURI, ToURI: toURI,
		RemoteTarget: remoteTarget, CSeq: cseq, done: make(chan error, 1)}
	// ACK a successful INVITE before trusting the SDP answer. A 2xx creates a
	// dialog and must be acknowledged even if the negotiated media later turns
	// out to be unusable. Set active first so retransmitted 2xx responses can be
	// re-ACKed by readLoop while we finish media validation.
	c.mu.Lock()
	c.active = call
	c.dialing = false
	c.mu.Unlock()
	established = true
	if err := c.sendACK(call); err != nil {
		c.mu.Lock()
		if c.active == call {
			c.active = nil
		}
		c.mu.Unlock()
		return nil, err
	}
	// A 2xx can cross a CANCEL/ring-timeout on the wire. At that point a SIP
	// dialog exists and MUST be ACKed, but the user no longer wants the call.
	// Tear it down immediately instead of accidentally starting media after
	// the configured ring timeout.
	if err := ctx.Err(); err != nil {
		hctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		hangupErr := call.Hangup(hctx)
		cancel()
		if hangupErr != nil {
			return nil, fmt.Errorf("SIP call answered after cancellation: %w (cleanup BYE failed: %v)", err, hangupErr)
		}
		return nil, fmt.Errorf("SIP call answered after cancellation: %w", err)
	}
	codec, remote, err := parseAnswerSDP(string(res.Body))
	if err != nil {
		hctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		hangupErr := call.Hangup(hctx)
		cancel()
		if hangupErr != nil {
			return nil, fmt.Errorf("invalid SIP media answer: %w (cleanup BYE failed: %v)", err, hangupErr)
		}
		return nil, fmt.Errorf("invalid SIP media answer: %w", err)
	}
	call.Codec = codec
	call.UpdateRemoteRTP(remote)
	if c.log != nil {
		c.log.Info("SIP call established", "destination", destination, "codec", codec.Name, "rtp", remote.String())
	}
	return call, nil
}

func (c *Client) sendInviteFinalACK(uri, branch string, cseq uint32, callID, fromURI, toURI, fromTag, toTag string) error {
	headers := c.baseHeaders("ACK", uri, branch, cseq, callID, fromURI, toURI, fromTag, toTag)
	msg := buildRequest("ACK", uri, headers, nil)
	target, err := c.destinationForURI(uri)
	if err != nil {
		return err
	}
	_, err = c.conn.WriteToUDP(msg, target)
	return err
}

func (c *Client) sendACK(call *Call) error {
	// CSeq is also advanced by Hangup. Copy it under the same mutex used by
	// BYE generation so a retransmitted 2xx response cannot race with a local
	// hangup while we build the ACK.
	c.mu.Lock()
	cseq := call.CSeq
	c.mu.Unlock()
	branch := branchID()
	headers := c.baseHeaders("ACK", call.RemoteTarget, branch, cseq, call.CallID, call.FromURI, call.ToURI, call.FromTag, call.ToTag)
	msg := buildRequest("ACK", call.RemoteTarget, headers, nil)
	target, err := c.destinationForURI(call.RemoteTarget)
	if err != nil {
		return err
	}
	_, err = c.conn.WriteToUDP(msg, target)
	return err
}

func (call *Call) Hangup(ctx context.Context) error {
	call.client.mu.Lock()
	call.CSeq++
	cseq := call.CSeq
	call.client.mu.Unlock()
	branch := branchID()
	headers := call.client.baseHeaders("BYE", call.RemoteTarget, branch, cseq, call.CallID, call.FromURI, call.ToURI, call.FromTag, call.ToTag)
	res, err := call.client.transact(ctx, "BYE", branch, cseq, call.RemoteTarget, headers, nil, false)
	if err == nil && (res.StatusCode < 200 || res.StatusCode >= 300) {
		err = fmt.Errorf("BYE returned %d", res.StatusCode)
	}
	call.finish(err)
	return err
}
func (call *Call) Done() <-chan error { return call.done }
func (call *Call) finish(err error) {
	call.doneOnce.Do(func() {
		call.client.mu.Lock()
		if call.client.active == call {
			call.client.active = nil
		}
		call.client.mu.Unlock()
		if call.incoming != nil {
			call.incoming.stopRetransmission()
		}
		call.done <- err
		close(call.done)
	})
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.registered.Store(false)
		_ = c.conn.Close()
		c.mu.Lock()
		call := c.active
		c.mu.Unlock()
		if call != nil {
			call.finish(errors.New("SIP client closed"))
		}
	})
}

func (c *Client) transact(ctx context.Context, method, branch string, cseq uint32, uri string, headers []string, body []byte, invite bool) (Message, error) {
	key := branch + "|" + method
	ch := make(chan Message, 8)
	c.mu.Lock()
	c.transactions[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.transactions, key); c.mu.Unlock() }()
	msg := buildRequest(method, uri, headers, body)
	target, err := c.destinationForURI(uri)
	if err != nil {
		return Message{}, err
	}
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	timerC := timer.C
	deadline := time.NewTimer(32 * time.Second)
	defer deadline.Stop()
	interval := 500 * time.Millisecond
	ctxDone := ctx.Done()
	cancelRequested := false
	cancelSent := false
	receivedResponse := false
	if _, err := c.conn.WriteToUDP(msg, target); err != nil {
		return Message{}, err
	}
	for {
		select {
		case res := <-ch:
			receivedResponse = true
			if res.StatusCode < 200 {
				if c.log != nil {
					c.log.Debug("SIP provisional", "code", res.StatusCode, "reason", res.Reason)
				}
				if invite && timerC != nil {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timerC = nil
				}
				if cancelRequested && !cancelSent {
					if err := c.sendCancel(uri, branch, cseq, headers); err != nil {
						return Message{}, fmt.Errorf("cancel SIP INVITE: %w", err)
					}
					cancelSent = true
				}
				continue
			}
			return res, nil
		case <-timerC:
			if _, err := c.conn.WriteToUDP(msg, target); err != nil {
				return Message{}, err
			}
			if interval < 4*time.Second {
				interval *= 2
			}
			timer.Reset(interval)
		case <-deadline.C:
			if cancelRequested && ctx.Err() != nil {
				return Message{}, fmt.Errorf("SIP %s cancellation timed out: %w", method, ctx.Err())
			}
			return Message{}, fmt.Errorf("SIP %s transaction timed out", method)
		case <-ctxDone:
			if !invite {
				return Message{}, ctx.Err()
			}
			// CANCEL and INVITE are separate SIP transactions. Keep the original
			// INVITE transaction around long enough to receive the expected final
			// response (normally 487) so Dial can ACK it. RFC 3261 also requires
			// waiting for at least one response to the INVITE before sending CANCEL.
			cancelRequested = true
			ctxDone = nil
			if receivedResponse && !cancelSent {
				if err := c.sendCancel(uri, branch, cseq, headers); err != nil {
					return Message{}, fmt.Errorf("cancel SIP INVITE: %w", err)
				}
				cancelSent = true
			}
			if !deadline.Stop() {
				select {
				case <-deadline.C:
				default:
				}
			}
			deadline.Reset(5 * time.Second)
		case <-c.closed:
			return Message{}, errors.New("SIP client closed")
		}
	}
}

func (c *Client) sendCancel(uri, branch string, cseq uint32, inviteHeaders []string) error {
	var via, from, to, callID string
	for _, h := range inviteHeaders {
		l := strings.ToLower(h)
		switch {
		case strings.HasPrefix(l, "via:"):
			via = h
		case strings.HasPrefix(l, "from:"):
			from = h
		case strings.HasPrefix(l, "to:"):
			to = h
		case strings.HasPrefix(l, "call-id:"):
			callID = h
		}
	}
	headers := []string{via, "Max-Forwards: 70", from, to, callID, fmt.Sprintf("CSeq: %d CANCEL", cseq)}
	packet := buildRequest("CANCEL", uri, headers, nil)
	target, err := c.destinationForURI(uri)
	if err != nil {
		return err
	}
	if _, err := c.conn.WriteToUDP(packet, target); err != nil {
		return err
	}
	// The INVITE transaction is about to be released when the caller's ring
	// timeout fires. Retransmit CANCEL a few times on UDP so a single lost
	// datagram does not leave the phone ringing until the PBX transaction dies.
	go func() {
		for _, delay := range []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond} {
			t := time.NewTimer(delay)
			select {
			case <-c.closed:
				if !t.Stop() {
					<-t.C
				}
				return
			case <-t.C:
				_, _ = c.conn.WriteToUDP(packet, target)
			}
		}
	}()
	return nil
}

func (c *Client) readLoop() {
	buf := make([]byte, 65535)
	for {
		n, addr, err := c.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-c.closed:
				return
			default:
			}
			if c.log != nil {
				c.log.Error("SIP read failed", "error", err)
			}
			c.Close()
			return
		}
		m, err := parseMessage(buf[:n])
		if err != nil {
			if c.log != nil {
				c.log.Warn("invalid SIP packet", "error", err)
			}
			continue
		}
		if c.cfg.Debug && c.log != nil {
			c.log.Debug("SIP packet", "from", addr.String(), "response", m.IsResponse, "method", m.Method, "status", m.StatusCode)
		}
		if m.IsResponse {
			branch := param(m.Header("via"), "branch")
			method := cseqMethod(m.Header("cseq"))
			key := branch + "|" + method
			c.mu.Lock()
			ch := c.transactions[key]
			c.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default:
				}
			} else if method == "INVITE" && m.StatusCode >= 200 && m.StatusCode < 300 {
				// A 2xx to INVITE is retransmitted independently of the INVITE
				// transaction. Re-ACK it if our first ACK was lost.
				c.mu.Lock()
				call := c.active
				callCSeq := uint32(0)
				if call != nil {
					callCSeq = call.CSeq
				}
				c.mu.Unlock()
				if call != nil && !call.inbound && m.Header("call-id") == call.CallID && cseqNumber(m.Header("cseq")) == callCSeq {
					_ = c.sendACK(call)
				}
			}
			continue
		}
		c.handleRequest(m, addr)
	}
}

func (c *Client) handleRequest(m Message, addr *net.UDPAddr) {
	switch strings.ToUpper(m.Method) {
	case "INVITE":
		c.handleIncomingInvite(m, addr)
	case "ACK":
		c.handleIncomingACK(m, addr)
	case "CANCEL":
		c.handleIncomingCancel(m, addr)
	case "BYE":
		c.mu.Lock()
		call := c.active
		c.mu.Unlock()
		if call == nil || m.Header("call-id") != call.CallID {
			_ = c.sendResponse(m, addr, 481, "Call/Transaction Does Not Exist")
			return
		}
		if call.inbound && !c.isRegistrarSource(addr) {
			_ = c.sendResponse(m, addr, 403, "Forbidden")
			return
		}
		_ = c.sendResponse(m, addr, 200, "OK")
		call.finish(nil)
	case "OPTIONS", "NOTIFY":
		_ = c.sendResponse(m, addr, 200, "OK")
	default:
		_ = c.sendResponse(m, addr, 501, "Not Implemented")
	}
}
func (c *Client) sendResponse(req Message, addr *net.UDPAddr, code int, reason string) error {
	lines := []string{fmt.Sprintf("SIP/2.0 %d %s", code, reason)}
	for _, name := range []string{"via", "from", "to", "call-id", "cseq"} {
		for _, v := range req.Headers[name] {
			lines = append(lines, canonical(name)+": "+v)
		}
	}
	lines = append(lines, "Content-Length: 0", "", "")
	_, err := c.conn.WriteToUDP([]byte(strings.Join(lines, "\r\n")), addr)
	return err
}

func (c *Client) baseHeaders(method, uri, branch string, cseq uint32, callID, fromURI, toURI, fromTag, toTag string) []string {
	to := fmt.Sprintf("<%s>", toURI)
	if toTag != "" {
		to += ";tag=" + toTag
	}
	from := fmt.Sprintf("\"%s\" <%s>;tag=%s", escapeDisplay(c.cfg.DisplayName), fromURI, fromTag)
	return []string{
		fmt.Sprintf("Via: SIP/2.0/UDP %s:%d;branch=%s;rport", c.localIP, c.cfg.LocalPort, branch),
		"Max-Forwards: 70", fromLine(from), "To: " + to, "Call-ID: " + callID, fmt.Sprintf("CSeq: %d %s", cseq, method),
		fmt.Sprintf("Contact: <sip:%s@%s:%d;transport=udp>", c.cfg.Username, c.localIP, c.cfg.LocalPort),
		"User-Agent: ReolinkSIPGateway/0.9.0",
	}
}
func fromLine(v string) string      { return "From: " + v }
func escapeDisplay(s string) string { return strings.ReplaceAll(s, "\"", "'") }

func (c *Client) offerSDP(port int) string {
	pts := "8 0 101"
	maps := []string{"a=rtpmap:8 PCMA/8000", "a=rtpmap:0 PCMU/8000"}
	if c.cfg.CodecPreference == "pcmu" {
		pts = "0 8 101"
		maps = []string{"a=rtpmap:0 PCMU/8000", "a=rtpmap:8 PCMA/8000"}
	}
	return strings.Join([]string{"v=0", fmt.Sprintf("o=- %d 1 IN IP4 %s", time.Now().Unix(), c.localIP), "s=Reolink SIP Gateway", fmt.Sprintf("c=IN IP4 %s", c.localIP), "t=0 0", fmt.Sprintf("m=audio %d RTP/AVP %s", port, pts), maps[0], maps[1], "a=rtpmap:101 telephone-event/8000", "a=fmtp:101 0-16", "a=ptime:20", "a=sendrecv", ""}, "\r\n")
}

func buildRequest(method, uri string, headers []string, body []byte) []byte {
	lines := []string{fmt.Sprintf("%s %s SIP/2.0", method, uri)}
	lines = append(lines, headers...)
	lines = append(lines, fmt.Sprintf("Content-Length: %d", len(body)), "", "")
	b := []byte(strings.Join(lines, "\r\n"))
	return append(b, body...)
}

func parseMessage(b []byte) (Message, error) {
	s := string(b)
	sep := strings.Index(s, "\r\n\r\n")
	if sep < 0 {
		return Message{}, errors.New("missing SIP header terminator")
	}
	head := s[:sep]
	body := []byte(s[sep+4:])
	lines := strings.Split(head, "\r\n")
	if len(lines) == 0 {
		return Message{}, errors.New("empty SIP")
	}
	m := Message{Headers: make(map[string][]string), Body: body, Raw: s}
	first := strings.Fields(lines[0])
	if len(first) < 2 {
		return Message{}, errors.New("bad start line")
	}
	if strings.HasPrefix(first[0], "SIP/") {
		m.IsResponse = true
		m.StatusCode, _ = strconv.Atoi(first[1])
		if len(first) > 2 {
			m.Reason = strings.Join(first[2:], " ")
		}
	} else {
		m.Method = first[0]
		m.URI = first[1]
	}
	var last string
	for _, line := range lines[1:] {
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') && last != "" {
			i := len(m.Headers[last]) - 1
			m.Headers[last][i] += " " + strings.TrimSpace(line)
			continue
		}
		p := strings.SplitN(line, ":", 2)
		if len(p) != 2 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(p[0]))
		switch name {
		case "v":
			name = "via"
		case "f":
			name = "from"
		case "t":
			name = "to"
		case "i":
			name = "call-id"
		case "l":
			name = "content-length"
		case "m":
			name = "contact"
		}
		m.Headers[name] = append(m.Headers[name], strings.TrimSpace(p[1]))
		last = name
	}
	if !m.IsResponse && m.Method == "" {
		return Message{}, errors.New("missing method")
	}
	return m, nil
}
func (m Message) Header(name string) string {
	v := m.Headers[strings.ToLower(name)]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

type parsedAudioSDP struct {
	payloads []int
	rtpmap   map[int]string
	remote   *net.UDPAddr
}

func parseAnswerSDP(s string) (Codec, *net.UDPAddr, error) {
	parsed, err := parseAudioSDP(s)
	if err != nil {
		return Codec{}, nil, fmt.Errorf("SIP answer %w", err)
	}
	for _, pt := range parsed.payloads {
		if codec, ok := codecForPayload(pt, parsed.rtpmap); ok {
			return codec, parsed.remote, nil
		}
	}
	return Codec{}, nil, errors.New("SIP answer did not select PCMA/PCMU")
}

func parseOfferSDP(s, preference string) (Codec, *net.UDPAddr, error) {
	parsed, err := parseAudioSDP(s)
	if err != nil {
		return Codec{}, nil, fmt.Errorf("SIP offer %w", err)
	}
	order := []string{"pcma", "pcmu"}
	if strings.EqualFold(strings.TrimSpace(preference), "pcmu") {
		order = []string{"pcmu", "pcma"}
	}
	for _, wanted := range order {
		for _, pt := range parsed.payloads {
			codec, ok := codecForPayload(pt, parsed.rtpmap)
			if ok && codec.Name == wanted {
				return codec, parsed.remote, nil
			}
		}
	}
	return Codec{}, nil, errors.New("SIP offer contains neither PCMA nor PCMU")
}

func parseAudioSDP(s string) (parsedAudioSDP, error) {
	parsed := parsedAudioSDP{rtpmap: make(map[int]string)}
	var sessionIP, audioIP string
	port := 0
	inAudio := false
	beforeMedia := true
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "m=audio "):
			f := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(f) >= 4 && port == 0 {
				inAudio = true
				port, _ = strconv.Atoi(f[1])
				for _, value := range f[3:] {
					if pt, convErr := strconv.Atoi(value); convErr == nil && pt >= 0 && pt <= 127 {
						parsed.payloads = append(parsed.payloads, pt)
					}
				}
			} else {
				inAudio = false
			}
			beforeMedia = false
		case strings.HasPrefix(line, "m="):
			inAudio = false
			beforeMedia = false
		case strings.HasPrefix(line, "c=IN IP4 "):
			candidate := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 ")))
			if len(candidate) > 0 {
				if inAudio {
					audioIP = candidate[0]
				} else if beforeMedia && sessionIP == "" {
					sessionIP = candidate[0]
				}
			}
		case inAudio && strings.HasPrefix(line, "a=rtpmap:"):
			rest := strings.TrimPrefix(line, "a=rtpmap:")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) == 2 {
				if pt, convErr := strconv.Atoi(parts[0]); convErr == nil {
					parsed.rtpmap[pt] = strings.ToUpper(strings.SplitN(parts[1], "/", 2)[0])
				}
			}
		}
	}
	if port < 1 || port > 65535 || len(parsed.payloads) == 0 {
		return parsedAudioSDP{}, errors.New("has no usable audio media")
	}
	ipText := audioIP
	if ipText == "" {
		ipText = sessionIP
	}
	if ipText == "" || ipText == "0.0.0.0" {
		return parsedAudioSDP{}, errors.New("has no usable audio address")
	}
	ip := net.ParseIP(ipText)
	if ip == nil {
		resolved, resolveErr := net.ResolveIPAddr("ip4", ipText)
		if resolveErr != nil || resolved.IP == nil {
			return parsedAudioSDP{}, fmt.Errorf("cannot resolve RTP address %q: %w", ipText, resolveErr)
		}
		ip = resolved.IP
	}
	parsed.remote = &net.UDPAddr{IP: ip, Port: port}
	return parsed, nil
}

func codecForPayload(pt int, rtpmap map[int]string) (Codec, bool) {
	name := rtpmap[pt]
	if name == "" {
		switch pt {
		case 8:
			name = "PCMA"
		case 0:
			name = "PCMU"
		}
	}
	if name != "PCMA" && name != "PCMU" {
		return Codec{}, false
	}
	return Codec{Name: strings.ToLower(name), PayloadType: uint8(pt)}, true
}

func param(h, name string) string {
	for _, p := range strings.Split(h, ";")[1:] {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) == 2 && strings.EqualFold(kv[0], name) {
			return strings.Trim(kv[1], "\"")
		}
	}
	return ""
}
func extractURI(h string) string {
	start := strings.Index(h, "<")
	end := strings.Index(h, ">")
	if start >= 0 && end > start {
		return h[start+1 : end]
	}
	if i := strings.Index(h, ";"); i >= 0 {
		return strings.TrimSpace(h[:i])
	}
	return strings.TrimSpace(h)
}
func cseqMethod(h string) string {
	f := strings.Fields(h)
	if len(f) > 1 {
		return strings.ToUpper(f[1])
	}
	return ""
}
func cseqNumber(h string) uint32 {
	f := strings.Fields(h)
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.ParseUint(f[0], 10, 32)
	return uint32(n)
}
func (c *Client) destinationForURI(uri string) (*net.UDPAddr, error) {
	u := strings.TrimSpace(extractURI(uri))
	lower := strings.ToLower(u)
	if strings.HasPrefix(lower, "sips:") {
		return nil, errors.New("SIPS/TLS is not supported; configure a SIP/UDP registrar")
	}
	if strings.HasPrefix(lower, "sip:") {
		u = u[4:]
	}
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	if i := strings.IndexByte(u, ';'); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndexByte(u, '@'); i >= 0 {
		u = u[i+1:]
	}
	u = strings.TrimSpace(u)
	if u == "" {
		return nil, errors.New("empty SIP destination")
	}
	host, port := u, c.cfg.RegistrarPort
	if strings.HasPrefix(u, "[") {
		if h, p, err := net.SplitHostPort(u); err == nil {
			host = h
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		} else {
			host = strings.Trim(u, "[]")
		}
	} else if strings.Count(u, ":") == 1 {
		if h, p, err := net.SplitHostPort(u); err == nil {
			host = h
			if n, err := strconv.Atoi(p); err == nil {
				port = n
			}
		}
	}
	addr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("resolve SIP destination %q: %w", uri, err)
	}
	return addr, nil
}

func canonical(s string) string {
	switch s {
	case "call-id":
		return "Call-ID"
	case "cseq":
		return "CSeq"
	default:
		return strings.ToUpper(s[:1]) + s[1:]
	}
}
func branchID() string { return "z9hG4bK-" + randomID() }
func randomID() string { b := make([]byte, 8); _, _ = rand.Read(b); return hex.EncodeToString(b) }

func (call *Call) ClientLocalIP() net.IP { return call.client.LocalIP() }
func (call *Call) IsInbound() bool       { return call != nil && call.inbound }

func (call *Call) RemoteRTPAddr() *net.UDPAddr {
	call.rtpMu.RLock()
	defer call.rtpMu.RUnlock()
	if call.RemoteRTP == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), call.RemoteRTP.IP...), Port: call.RemoteRTP.Port}
}

func (call *Call) UpdateRemoteRTP(addr *net.UDPAddr) {
	if addr == nil {
		return
	}
	call.rtpMu.Lock()
	call.RemoteRTP = &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port}
	call.rtpMu.Unlock()
}
