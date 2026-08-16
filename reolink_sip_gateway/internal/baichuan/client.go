// Portions adapted from Shareed2k/reolinkproxy (MIT License).
// Copyright (c) 2026 Roman Kredentser. See THIRD-PARTY-NOTICES.md.
package baichuan

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

type Client struct {
	cfg       Config
	transport net.Conn
	sendMu    sync.Mutex
	seqMu     sync.Mutex
	msgNum    uint16

	stateMu   sync.RWMutex
	mode      EncryptionMode
	aesKey    [16]byte
	hasAESKey bool

	binaryMu      sync.RWMutex
	binaryMsgNums map[uint16]struct{}

	loginMu  sync.Mutex
	loggedIn bool

	pendingMu sync.Mutex
	pending   map[pendingKey]chan *Message

	subMu sync.RWMutex
	subs  map[uint32]map[*subscription]struct{}

	closed        chan struct{}
	closeOnce     sync.Once
	closeErr      closeState
	wg            sync.WaitGroup
	keepAliveOnce sync.Once
}

func Dial(ctx context.Context, cfg Config) (*Client, error) {
	cfg = cfg.normalized()
	host := cfg.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	}
	dialer := net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	c := &Client{
		cfg:           cfg,
		transport:     conn,
		mode:          EncryptionNone,
		binaryMsgNums: make(map[uint16]struct{}),
		pending:       make(map[pendingKey]chan *Message),
		subs:          make(map[uint32]map[*subscription]struct{}),
		closed:        make(chan struct{}),
	}
	c.wg.Add(1)
	go c.readLoop()
	return c, nil
}

func (c *Client) readLoop() {
	defer c.wg.Done()
	for {
		msg, err := c.readMessage()
		if err != nil {
			c.shutdown(err)
			return
		}
		key := pendingKey{msgID: msg.Header.MsgID, msgNum: msg.Header.MsgNum}
		c.pendingMu.Lock()
		ch := c.pending[key]
		c.pendingMu.Unlock()
		if ch != nil {
			select {
			case ch <- msg:
			default:
			}
		}

		// Preview media arrives asynchronously with msg_id=3 and therefore has
		// no pending request waiter. Fan it out to subscribers without ever
		// blocking the protocol read loop.
		c.subMu.RLock()
		var subs []*subscription
		for sub := range c.subs[msg.Header.MsgID] {
			subs = append(subs, sub)
		}
		c.subMu.RUnlock()
		for _, sub := range subs {
			select {
			case sub.ch <- msg:
			default:
				if sub.reliable {
					c.shutdown(fmt.Errorf("Baichuan media subscriber overflow for msg_id=%d", msg.Header.MsgID))
					return
				}
			}
		}
	}
}

func (c *Client) shutdown(err error) {
	c.closeOnce.Do(func() {
		c.closeErr.set(err)
		close(c.closed)
		_ = c.transport.Close()
	})
}

func (c *Client) Close() error {
	c.shutdown(context.Canceled)
	c.wg.Wait()
	return nil
}
func (c *Client) Err() error            { return c.closeErr.get() }
func (c *Client) Done() <-chan struct{} { return c.closed }

func (c *Client) Login(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if c.loggedIn {
		return nil
	}

	nonceResp, err := c.sendRequest(ctx, request{MsgID: msgIDLogin, Class: classLegacy, ForceBC: true})
	if err != nil {
		return fmt.Errorf("request nonce: %w", err)
	}
	nonce, err := parseNonce(nonceResp.XML)
	if err != nil {
		snippet := nonceResp.XML
		if len(snippet) > 160 {
			snippet = snippet[:160]
		}
		return fmt.Errorf("parse nonce: %w (response_code=%#x class=%#x xml_prefix=%q)", err, nonceResp.Header.ResponseCode, nonceResp.Header.Class, snippet)
	}

	c.stateMu.Lock()
	c.aesKey = DeriveAESKey(nonce, c.cfg.Password)
	c.hasAESKey = true
	c.stateMu.Unlock()

	body, err := buildLoginXML(MD5Modern(c.cfg.Username+nonce), MD5Modern(c.cfg.Password+nonce))
	if err != nil {
		return err
	}
	if _, err := c.sendRequest(ctx, request{MsgID: msgIDLogin, Class: classModernWithOffset, Body: body, ForceBC: true}); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	c.stateMu.Lock()
	if c.hasAESKey {
		c.mode = EncryptionAES
	}
	c.stateMu.Unlock()
	c.loggedIn = true
	c.keepAliveOnce.Do(func() { c.wg.Add(1); go c.keepAliveLoop() })
	return nil
}

func (c *Client) keepAliveLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = c.sendNoReply(request{MsgID: msgIDPing, Class: classModernWithOffset})
		case <-c.closed:
			return
		}
	}
}

type subscription struct {
	ch       chan *Message
	reliable bool
}

// Subscribe attaches a best-effort fanout listener for asynchronous messages.
// It is intended for low-rate control/event traffic. Media preview uses the
// reliable variant below because dropping one arbitrary byte range would
// corrupt the stateful bcmedia stream.
func (c *Client) Subscribe(msgID uint32) (<-chan *Message, func()) {
	return c.subscribe(msgID, 64, false)
}

func (c *Client) subscribe(msgID uint32, buffer int, reliable bool) (<-chan *Message, func()) {
	if buffer < 1 {
		buffer = 1
	}
	sub := &subscription{ch: make(chan *Message, buffer), reliable: reliable}
	c.subMu.Lock()
	if c.subs[msgID] == nil {
		c.subs[msgID] = make(map[*subscription]struct{})
	}
	c.subs[msgID][sub] = struct{}{}
	c.subMu.Unlock()

	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			c.subMu.Lock()
			if subs := c.subs[msgID]; subs != nil {
				delete(subs, sub)
				if len(subs) == 0 {
					delete(c.subs, msgID)
				}
			}
			c.subMu.Unlock()
		})
	}
}

// StartPreview starts a Baichuan live preview session and returns parsed media
// packets. The stream is carried by msg_id=3 over the same authenticated TCP
// connection; no RTSP server or URL is involved.
func (c *Client) StartPreview(ctx context.Context, channel uint8, stream Stream) (*MediaReader, error) {
	return c.startPreview(ctx, channel, stream, false)
}

// StartAudioPreview is the audio-only variant used by the SIP gateway. It still
// parses and advances across video frames in the multiplexed bcmedia stream,
// but deliberately avoids copying/publishing their payloads. This keeps video
// bandwidth from becoming avoidable memory pressure or audio latency.
func (c *Client) StartAudioPreview(ctx context.Context, channel uint8, stream Stream) (*MediaReader, error) {
	return c.startPreview(ctx, channel, stream, true)
}

func (c *Client) startPreview(ctx context.Context, channel uint8, stream Stream, audioOnly bool) (*MediaReader, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	streamType, handle := streamParams(stream)
	body, err := buildPreviewXML(channel, handle, stream)
	if err != nil {
		return nil, err
	}
	sub, unsubscribe := c.subscribe(msgIDVideo, 256, true)
	if _, err := c.sendRequest(ctx, request{
		MsgID: msgIDVideo, ChannelID: channel, StreamType: streamType,
		Class: classModernWithOffset, Body: body,
	}); err != nil {
		unsubscribe()
		return nil, err
	}

	packets := make(chan MediaPacket, 128)
	stop := make(chan struct{})
	reader := &MediaReader{Packets: packets, client: c, channel: channel, stream: stream, stop: stop}
	var stopOnce sync.Once
	reader.stopOnce = func() { stopOnce.Do(func() { close(stop) }) }

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer unsubscribe()
		defer close(packets)
		parser := MediaParser{AudioOnly: audioOnly}
		for {
			select {
			case <-c.closed:
				return
			case <-stop:
				return
			case msg := <-sub:
				if msg == nil || !msg.Binary || len(msg.Payload) == 0 {
					continue
				}
				if msg.Header.StreamType != streamType {
					continue
				}
				parsed, err := parser.Append(msg.Payload)
				if err != nil {
					prefixLen := len(msg.Payload)
					if prefixLen > 32 {
						prefixLen = 32
					}
					c.shutdown(fmt.Errorf("bcmedia parse: %w (payload_prefix=%x)", err, msg.Payload[:prefixLen]))
					return
				}
				for _, packet := range parsed {
					select {
					case packets <- packet:
					case <-c.closed:
						return
					case <-stop:
						return
					}
				}
			}
		}
	}()
	return reader, nil
}

// StopPreview requests the NVR/camera to stop one Baichuan preview stream.
func (c *Client) StopPreview(ctx context.Context, channel uint8, stream Stream) error {
	if err := c.Login(ctx); err != nil {
		return err
	}
	streamType, handle := streamParams(stream)
	body, err := buildStopPreviewXML(channel, handle)
	if err != nil {
		return err
	}
	resp, err := c.sendRequest(ctx, request{
		MsgID: msgIDVideoStop, ChannelID: channel, StreamType: streamType,
		Class: classModernWithOffset, Body: body,
	})
	if err != nil {
		if _, ok := err.(*StatusError); ok {
			return err
		}
		return nil
	}
	return resp.success()
}

func streamParams(stream Stream) (uint8, uint32) {
	switch stream {
	case StreamSub:
		return 1, 256
	case StreamExtern:
		return 2, 1024
	default:
		return 0, 0
	}
}

func (c *Client) sendRequest(ctx context.Context, req request) (*Message, error) {
	msg, err := c.roundTripRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := msg.success(); err != nil {
		return nil, err
	}
	return msg, nil
}

func (c *Client) roundTripRequest(ctx context.Context, req request) (*Message, error) {
	req.MsgNum = c.reserveMessageNumber()
	key := pendingKey{msgID: req.MsgID, msgNum: req.MsgNum}
	ch := make(chan *Message, 1)
	c.pendingMu.Lock()
	c.pending[key] = ch
	c.pendingMu.Unlock()
	defer func() { c.pendingMu.Lock(); delete(c.pending, key); c.pendingMu.Unlock() }()
	if err := c.writeRequest(req); err != nil {
		return nil, err
	}
	select {
	case msg := <-ch:
		return msg, nil
	case <-c.closed:
		// A peer may close immediately after writing a valid response. The read
		// loop publishes the response before observing EOF on its next read, so
		// prefer that already-buffered response over the terminal connection
		// state. This avoids a false EOF on successful final commands.
		select {
		case msg := <-ch:
			return msg, nil
		default:
		}
		if err := c.closeErr.get(); err != nil {
			return nil, err
		}
		return nil, context.Canceled
	case <-ctx.Done():
		select {
		case msg := <-ch:
			return msg, nil
		default:
		}
		return nil, ctx.Err()
	}
}

func (c *Client) sendNoReply(req request) error {
	req.MsgNum = c.reserveMessageNumber()
	return c.writeRequest(req)
}
func (c *Client) writeRequest(req request) error {
	payload := c.encodeRequest(req)
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.cfg.Timeout > 0 {
		writeTimeout := c.cfg.Timeout
		if writeTimeout > 2*time.Second {
			writeTimeout = 2 * time.Second
		}
		_ = c.transport.SetWriteDeadline(time.Now().Add(writeTimeout))
		defer c.transport.SetWriteDeadline(time.Time{})
	}
	_, err := c.transport.Write(payload)
	return err
}
func (c *Client) reserveMessageNumber() uint16 {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	n := c.msgNum
	c.msgNum++
	return n
}
func (c *Client) snapshotCipher() (EncryptionMode, [16]byte, bool) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.mode, c.aesKey, c.hasAESKey
}
func (c *Client) setNegotiatedEncryption(code uint16) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	switch byte(code) {
	case 0x00:
		c.mode = EncryptionNone
	case 0x01, 0x12:
		c.mode = EncryptionBC
	case 0x02, 0x03:
		c.mode = EncryptionAES
	}
}
