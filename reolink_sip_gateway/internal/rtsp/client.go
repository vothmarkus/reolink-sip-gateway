package rtsp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/digest"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
)

type Backchannel struct {
	Codec       string
	PayloadType uint8
	ControlURL  string
}

type Client struct {
	logger   *slog.Logger
	debug    bool
	username string
	password string
	rtspURL  string

	conn           net.Conn
	reader         *bufio.Reader
	writeMu        sync.Mutex
	cseq           atomic.Uint32
	session        string
	baseURI        string
	sessionTimeout time.Duration
	auth           *digest.Challenge
	nc             atomic.Uint32

	responsesMu sync.Mutex
	responses   map[uint32]chan response
	closed      chan struct{}
	closeOnce   sync.Once
	readErr     chan error

	rtpChannel  byte
	rtcpChannel byte
	mediaMu     sync.Mutex
	ssrc        uint32
	seq         uint16
	timestamp   uint32
	packets     uint32
	octets      uint32
}

type response struct {
	StatusCode   int
	Reason       string
	Header       map[string]string
	HeaderValues map[string][]string
	Body         []byte
}

const maxRTSPBodySize = 4 << 20 // 4 MiB; SDP/control responses are normally tiny.

type sdpMedia struct {
	Kind      string
	Payloads  []int
	Attrs     map[string][]string
	Control   string
	Direction string
	RTPMap    map[int]string
}

func New(rawURL, username, password string, logger *slog.Logger, debug bool) *Client {
	return &Client{
		logger:      logger,
		debug:       debug,
		username:    username,
		password:    password,
		rtspURL:     rawURL,
		responses:   make(map[uint32]chan response),
		closed:      make(chan struct{}),
		readErr:     make(chan error, 1),
		rtpChannel:  0,
		rtcpChannel: 1,
	}
}

func (c *Client) Open(ctx context.Context) (Backchannel, error) {
	u, err := url.Parse(c.rtspURL)
	if err != nil {
		return Backchannel{}, err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "554")
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return Backchannel{}, fmt.Errorf("connect RTSP: %w", err)
	}
	c.conn = conn
	c.reader = bufio.NewReader(conn)
	go c.readLoop()

	headers := map[string]string{
		"Accept":  "application/sdp",
		"Require": "www.onvif.org/ver20/backchannel",
	}
	res, err := c.doWithTimeout(ctx, 8*time.Second, "DESCRIBE", c.rtspURL, headers, nil)
	if err != nil {
		c.Close()
		return Backchannel{}, err
	}
	if res.StatusCode != 200 {
		c.Close()
		return Backchannel{}, fmt.Errorf("DESCRIBE returned %d %s", res.StatusCode, res.Reason)
	}
	base := res.Header["content-base"]
	if base == "" {
		base = c.rtspURL
	}
	c.baseURI = base
	bc, err := findBackchannel(string(res.Body), base)
	if err != nil {
		c.Close()
		return Backchannel{}, err
	}

	setupHeaders := map[string]string{
		"Transport": fmt.Sprintf("RTP/AVP/TCP;unicast;interleaved=%d-%d", c.rtpChannel, c.rtcpChannel),
		"Require":   "www.onvif.org/ver20/backchannel",
	}
	res, err = c.doWithTimeout(ctx, 8*time.Second, "SETUP", bc.ControlURL, setupHeaders, nil)
	if err != nil {
		c.Close()
		return Backchannel{}, err
	}
	if res.StatusCode != 200 {
		c.Close()
		return Backchannel{}, fmt.Errorf("SETUP returned %d %s", res.StatusCode, res.Reason)
	}
	c.session, c.sessionTimeout = parseSession(res.Header["session"])
	if c.session == "" {
		c.Close()
		return Backchannel{}, errors.New("SETUP response did not include an RTSP Session header")
	}
	if tr := res.Header["transport"]; tr != "" {
		parseInterleaved(tr, &c.rtpChannel, &c.rtcpChannel)
	}

	playHeaders := map[string]string{"Require": "www.onvif.org/ver20/backchannel"}
	res, err = c.doWithTimeout(ctx, 8*time.Second, "PLAY", base, playHeaders, nil)
	if err != nil {
		c.Close()
		return Backchannel{}, err
	}
	if res.StatusCode != 200 {
		c.Close()
		return Backchannel{}, fmt.Errorf("PLAY returned %d %s", res.StatusCode, res.Reason)
	}

	c.ssrc = randomUint32()
	c.seq = uint16(randomUint32())
	c.timestamp = randomUint32()
	go c.keepalive(ctx, base)
	go c.senderReports(ctx)
	return bc, nil
}

func (c *Client) WriteAudio(payload []byte, payloadType uint8) error {
	select {
	case <-c.closed:
		return errors.New("RTSP client closed")
	default:
	}
	c.mediaMu.Lock()
	p := rtp.Packet{
		PayloadType: payloadType,
		Sequence:    c.seq,
		Timestamp:   c.timestamp,
		SSRC:        c.ssrc,
		Payload:     payload,
	}
	c.seq++
	c.timestamp += uint32(len(payload))
	c.packets++
	c.octets += uint32(len(payload))
	c.mediaMu.Unlock()
	return c.writeInterleaved(c.rtpChannel, rtp.Marshal(p))
}

func (c *Client) WaitError() <-chan error { return c.readErr }

// Done is closed exactly once when the RTSP connection is no longer usable.
// It must be preferred for lifecycle waits because readErr is intentionally
// buffered and may be consumed by an in-flight request.
func (c *Client) Done() <-chan struct{} { return c.closed }

func (c *Client) Shutdown(ctx context.Context) error {
	select {
	case <-c.closed:
		return nil
	default:
	}
	var err error
	if c.conn != nil && c.session != "" && c.baseURI != "" {
		res, reqErr := c.doWithTimeout(ctx, 2*time.Second, "TEARDOWN", c.baseURI, map[string]string{"Require": "www.onvif.org/ver20/backchannel"}, nil)
		if reqErr != nil {
			err = reqErr
		} else if res.StatusCode != 200 && res.StatusCode != 454 {
			err = fmt.Errorf("TEARDOWN returned %d %s", res.StatusCode, res.Reason)
		}
	}
	c.Close()
	return err
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

func (c *Client) keepalive(ctx context.Context, uri string) {
	interval := 20 * time.Second
	if c.sessionTimeout > 0 {
		half := c.sessionTimeout / 2
		if half < interval {
			interval = half
		}
	}
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-t.C:
			res, err := c.doWithTimeout(ctx, 5*time.Second, "GET_PARAMETER", uri, map[string]string{"Require": "www.onvif.org/ver20/backchannel"}, nil)
			if err == nil && res.StatusCode >= 200 && res.StatusCode < 300 {
				failures = 0
				continue
			}
			// Some RTSP servers do not implement GET_PARAMETER. OPTIONS is a
			// standards-compatible keepalive fallback for the TCP session.
			res2, err2 := c.doWithTimeout(ctx, 5*time.Second, "OPTIONS", uri, nil, nil)
			if err2 == nil && res2.StatusCode >= 200 && res2.StatusCode < 300 {
				failures = 0
				continue
			}
			failures++
			if c.logger != nil {
				c.logger.Warn("RTSP keepalive failed", "attempt", failures, "get_parameter_error", err, "options_error", err2)
			}
			if failures >= 3 {
				c.signalReadErr(errors.New("RTSP keepalive failed three consecutive times"))
				return
			}
		}
	}
}

func (c *Client) senderReports(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case now := <-t.C:
			sec, frac := ntp(now)
			c.mediaMu.Lock()
			sr := rtp.SenderReport(c.ssrc, c.timestamp, c.packets, c.octets, sec, frac)
			c.mediaMu.Unlock()
			if err := c.writeInterleaved(c.rtcpChannel, sr); err != nil {
				c.signalReadErr(fmt.Errorf("write RTCP sender report: %w", err))
				return
			}
		}
	}
}

func ntp(t time.Time) (uint32, uint32) {
	const ntpEpochOffset = 2208988800
	sec := uint32(t.Unix() + ntpEpochOffset)
	frac := uint32((uint64(t.Nanosecond()) << 32) / 1_000_000_000)
	return sec, frac
}

func (c *Client) doWithTimeout(parent context.Context, timeout time.Duration, method, uri string, headers map[string]string, body []byte) (response, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return c.do(ctx, method, uri, headers, body)
}

func (c *Client) do(ctx context.Context, method, uri string, headers map[string]string, body []byte) (response, error) {
	res, err := c.doOnce(ctx, method, uri, headers, body, "")
	if err != nil {
		return response{}, err
	}
	if res.StatusCode != 401 {
		return res, nil
	}

	// Embedded cameras/NVRs are not always perfectly RFC-compliant. In
	// particular, a device may refresh its nonce after the first authenticated
	// request (stale=true) or advertise more than one supported digest
	// algorithm. Retry a bounded number of 401 challenges while preserving all
	// WWW-Authenticate header instances and never logging nonce/password data.
	const maxAuthRetries = 3
	for attempt := 1; attempt <= maxAuthRetries; attempt++ {
		challenges := responseHeaderValues(res, "www-authenticate")
		ch, parseErr := chooseDigestChallenge(challenges)
		if parseErr != nil {
			return res, fmt.Errorf("RTSP authentication: %w", parseErr)
		}
		nonceChanged := c.auth == nil || c.auth.Nonce != ch.Nonce || c.auth.Realm != ch.Realm || !strings.EqualFold(c.auth.Algorithm, ch.Algorithm)
		c.auth = &ch
		if nonceChanged {
			c.nc.Store(0)
		}
		if c.debug && c.logger != nil {
			c.logger.Debug("RTSP digest challenge", "method", method, "attempt", attempt, "realm", ch.Realm, "algorithm", ch.Algorithm, "qop", ch.QOP, "stale", ch.Stale, "challenge_count", len(challenges))
		}
		auth := digest.Authorization(ch, c.username, c.password, method, uri, c.nc.Add(1))
		res, err = c.doOnce(ctx, method, uri, headers, body, auth)
		if err != nil {
			return response{}, err
		}
		if res.StatusCode != 401 {
			return res, nil
		}
	}
	if c.debug && c.logger != nil {
		c.logger.Debug("RTSP digest authentication rejected after retries", "method", method, "status", res.StatusCode)
	}
	return res, nil
}

func responseHeaderValues(res response, name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if values := res.HeaderValues[name]; len(values) > 0 {
		return values
	}
	if v := res.Header[name]; v != "" {
		return []string{v}
	}
	return nil
}

func chooseDigestChallenge(values []string) (digest.Challenge, error) {
	var firstErr error
	for _, value := range values {
		ch, err := digest.Parse(value)
		if err == nil {
			return ch, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return digest.Challenge{}, firstErr
	}
	return digest.Challenge{}, errors.New("401 response did not include a supported WWW-Authenticate digest challenge")
}

func (c *Client) doOnce(ctx context.Context, method, uri string, headers map[string]string, body []byte, auth string) (response, error) {
	seq := c.cseq.Add(1)
	ch := make(chan response, 1)
	c.responsesMu.Lock()
	c.responses[seq] = ch
	c.responsesMu.Unlock()
	defer func() {
		c.responsesMu.Lock()
		delete(c.responses, seq)
		c.responsesMu.Unlock()
	}()

	var b bytes.Buffer
	fmt.Fprintf(&b, "%s %s RTSP/1.0\r\n", method, uri)
	fmt.Fprintf(&b, "CSeq: %d\r\n", seq)
	fmt.Fprintf(&b, "User-Agent: ReolinkSIPGateway/0.7.0\r\n")
	if c.session != "" && method != "DESCRIBE" {
		fmt.Fprintf(&b, "Session: %s\r\n", c.session)
	}
	if auth != "" {
		fmt.Fprintf(&b, "Authorization: %s\r\n", auth)
	} else if c.auth != nil {
		fmt.Fprintf(&b, "Authorization: %s\r\n", digest.Authorization(*c.auth, c.username, c.password, method, uri, c.nc.Add(1)))
	}
	for k, v := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	if len(body) > 0 {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", len(body))
	}
	b.WriteString("\r\n")
	b.Write(body)
	if c.debug && c.logger != nil {
		c.logger.Debug("RTSP request", "method", method, "uri", uri, "cseq", seq)
	}
	c.writeMu.Lock()
	_, err := c.conn.Write(b.Bytes())
	c.writeMu.Unlock()
	if err != nil {
		return response{}, err
	}
	select {
	case res := <-ch:
		return res, nil
	case err := <-c.readErr:
		// A server is allowed to close the TCP connection immediately after
		// sending a final response (notably after TEARDOWN). readLoop delivers
		// the parsed response before it can observe the subsequent EOF, but a
		// select here could otherwise choose readErr first. Prefer an already
		// delivered response so a successful RTSP transaction is not reported
		// as a spurious EOF.
		select {
		case res := <-ch:
			return res, nil
		default:
			return response{}, err
		}
	case <-ctx.Done():
		return response{}, ctx.Err()
	case <-c.closed:
		select {
		case res := <-ch:
			return res, nil
		default:
			return response{}, errors.New("RTSP connection closed")
		}
	}
}

func (c *Client) writeInterleaved(channel byte, payload []byte) error {
	frame := make([]byte, 4+len(payload))
	frame[0] = '$'
	frame[1] = channel
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(payload)))
	copy(frame[4:], payload)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.conn.Write(frame)
	return err
}

func (c *Client) readLoop() {
	for {
		first, err := c.reader.Peek(1)
		if err != nil {
			c.signalReadErr(err)
			return
		}
		if first[0] == '$' {
			header := make([]byte, 4)
			if _, err := io.ReadFull(c.reader, header); err != nil {
				c.signalReadErr(err)
				return
			}
			n := int(binary.BigEndian.Uint16(header[2:4]))
			if _, err := io.CopyN(io.Discard, c.reader, int64(n)); err != nil {
				c.signalReadErr(err)
				return
			}
			continue
		}
		res, err := readResponse(c.reader)
		if err != nil {
			c.signalReadErr(err)
			return
		}
		seq, _ := strconv.ParseUint(res.Header["cseq"], 10, 32)
		c.responsesMu.Lock()
		ch := c.responses[uint32(seq)]
		c.responsesMu.Unlock()
		if ch != nil {
			select {
			case ch <- res:
			default:
			}
		}
	}
}

func (c *Client) signalReadErr(err error) {
	select {
	case c.readErr <- err:
	default:
	}
	c.Close()
}

func readResponse(r *bufio.Reader) (response, error) {
	line, err := readLine(r)
	if err != nil {
		return response{}, err
	}
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "RTSP/") {
		return response{}, fmt.Errorf("unexpected RTSP line %q", line)
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return response{}, err
	}
	reason := ""
	if len(parts) == 3 {
		reason = parts[2]
	}
	h := make(map[string]string)
	hv := make(map[string][]string)
	for {
		line, err = readLine(r)
		if err != nil {
			return response{}, err
		}
		if line == "" {
			break
		}
		p := strings.SplitN(line, ":", 2)
		if len(p) == 2 {
			name := strings.ToLower(strings.TrimSpace(p[0]))
			value := strings.TrimSpace(p[1])
			h[name] = value
			hv[name] = append(hv[name], value)
		}
	}
	length := 0
	if raw := strings.TrimSpace(h["content-length"]); raw != "" {
		var err error
		length, err = strconv.Atoi(raw)
		if err != nil || length < 0 || length > maxRTSPBodySize {
			return response{}, fmt.Errorf("invalid RTSP Content-Length %q", raw)
		}
	}
	body := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, body); err != nil {
			return response{}, err
		}
	}
	return response{StatusCode: code, Reason: reason, Header: h, HeaderValues: hv, Body: body}, nil
}

func readLine(r *bufio.Reader) (string, error) {
	s, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

func findBackchannel(sdp, base string) (Backchannel, error) {
	medias := parseSDP(sdp)
	for _, m := range medias {
		if m.Kind != "audio" || m.Direction != "sendonly" {
			continue
		}
		for _, pt := range m.Payloads {
			name := strings.ToUpper(strings.SplitN(m.RTPMap[pt], "/", 2)[0])
			// RTP payload types 0 and 8 are statically assigned to PCMU and
			// PCMA. A standards-compliant SDP may therefore omit a=rtpmap.
			if name == "" {
				switch pt {
				case 0:
					name = "PCMU"
				case 8:
					name = "PCMA"
				}
			}
			codec := ""
			switch name {
			case "PCMA":
				codec = "pcma"
			case "PCMU":
				codec = "pcmu"
			}
			if codec != "" {
				return Backchannel{Codec: codec, PayloadType: uint8(pt), ControlURL: resolveControl(base, m.Control)}, nil
			}
		}
	}
	return Backchannel{}, errors.New("no G.711 ONVIF audio backchannel found in RTSP SDP")
}

func parseSDP(s string) []sdpMedia {
	var out []sdpMedia
	var cur *sdpMedia
	for _, raw := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "m=") {
			f := strings.Fields(strings.TrimPrefix(line, "m="))
			if len(f) >= 4 {
				m := sdpMedia{Kind: f[0], Attrs: make(map[string][]string), RTPMap: make(map[int]string)}
				for _, x := range f[3:] {
					if n, err := strconv.Atoi(x); err == nil {
						m.Payloads = append(m.Payloads, n)
					}
				}
				out = append(out, m)
				cur = &out[len(out)-1]
			}
			continue
		}
		if cur == nil || !strings.HasPrefix(line, "a=") {
			continue
		}
		a := strings.TrimPrefix(line, "a=")
		switch {
		case a == "sendonly" || a == "recvonly" || a == "sendrecv" || a == "inactive":
			cur.Direction = a
		case strings.HasPrefix(a, "control:"):
			cur.Control = strings.TrimSpace(strings.TrimPrefix(a, "control:"))
		case strings.HasPrefix(a, "rtpmap:"):
			rest := strings.TrimSpace(strings.TrimPrefix(a, "rtpmap:"))
			p := strings.SplitN(rest, " ", 2)
			if len(p) == 2 {
				pt, _ := strconv.Atoi(p[0])
				cur.RTPMap[pt] = strings.TrimSpace(p[1])
			}
		}
	}
	return out
}

func resolveControl(base, control string) string {
	if strings.HasPrefix(control, "rtsp://") || strings.HasPrefix(control, "rtsps://") {
		return control
	}
	bu, err := url.Parse(base)
	if err != nil {
		return base + "/" + strings.TrimPrefix(control, "/")
	}
	if strings.HasPrefix(control, "/") {
		bu.Path = control
		bu.RawQuery = ""
		return bu.String()
	}
	if !strings.HasSuffix(bu.Path, "/") {
		bu.Path += "/"
	}
	ref, _ := url.Parse(control)
	return bu.ResolveReference(ref).String()
}

func parseInterleaved(transport string, rtpCh, rtcpCh *byte) {
	for _, p := range strings.Split(transport, ";") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(strings.ToLower(p), "interleaved=") {
			vals := strings.SplitN(strings.TrimPrefix(strings.ToLower(p), "interleaved="), "-", 2)
			if len(vals) == 2 {
				a, _ := strconv.Atoi(vals[0])
				b, _ := strconv.Atoi(vals[1])
				*rtpCh, *rtcpCh = byte(a), byte(b)
			}
		}
	}
}

func parseSession(s string) (string, time.Duration) {
	parts := strings.Split(s, ";")
	id := strings.TrimSpace(parts[0])
	var timeout time.Duration
	for _, part := range parts[1:] {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 || !strings.EqualFold(kv[0], "timeout") {
			continue
		}
		if sec, err := strconv.Atoi(strings.TrimSpace(kv[1])); err == nil && sec > 0 {
			timeout = time.Duration(sec) * time.Second
		}
	}
	return id, timeout
}

func randomUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint32(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint32(b[:])
}
