package ha

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	wsOpcodeContinuation = 0x0
	wsOpcodeText         = 0x1
	wsOpcodeClose        = 0x8
	wsOpcodePing         = 0x9
	wsOpcodePong         = 0xA
	wsMaxMessageSize     = 16 << 20 // Large HA registries fit while every frame/message remains bounded.
)

type wsConn struct {
	conn    net.Conn
	reader  *bufio.Reader
	writeMu sync.Mutex
}

func dialWebSocket(ctx context.Context, rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse websocket URL: %w", err)
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported websocket scheme %q", u.Scheme)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "80")
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("connect websocket: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = conn.Close()
		}
	}()

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return nil, fmt.Errorf("websocket nonce: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	req := &http.Request{
		Method: http.MethodGet,
		URL:    &url.URL{Path: u.Path, RawQuery: u.RawQuery},
		Host:   u.Host,
		Header: make(http.Header),
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("write websocket handshake: %w", err)
	}

	reader := bufio.NewReader(conn)
	res, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, fmt.Errorf("read websocket handshake: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("websocket handshake returned %s", res.Status)
	}
	if !headerTokenContains(res.Header, "Connection", "upgrade") || !strings.EqualFold(strings.TrimSpace(res.Header.Get("Upgrade")), "websocket") {
		return nil, errors.New("invalid websocket upgrade response")
	}
	wantAccept := websocketAccept(key)
	if res.Header.Get("Sec-WebSocket-Accept") != wantAccept {
		return nil, errors.New("invalid websocket accept key")
	}

	closeOnError = false
	return &wsConn{conn: conn, reader: reader}, nil
}

func headerTokenContains(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (c *wsConn) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	_ = c.writeFrame(wsOpcodeClose, nil)
	return c.conn.Close()
}

func (c *wsConn) WriteJSON(payload []byte) error { return c.writeFrame(wsOpcodeText, payload) }
func (c *wsConn) Ping() error                    { return c.writeFrame(wsOpcodePing, nil) }

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	if len(payload) > wsMaxMessageSize {
		return errors.New("websocket payload too large")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var header [14]byte
	header[0] = 0x80 | opcode
	n := 2
	plen := len(payload)
	switch {
	case plen < 126:
		header[1] = 0x80 | byte(plen)
	case plen <= 0xFFFF:
		header[1] = 0x80 | 126
		binary.BigEndian.PutUint16(header[2:4], uint16(plen))
		n = 4
	default:
		header[1] = 0x80 | 127
		binary.BigEndian.PutUint64(header[2:10], uint64(plen))
		n = 10
	}
	mask := header[n : n+4]
	if _, err := rand.Read(mask); err != nil {
		return fmt.Errorf("websocket mask: %w", err)
	}
	n += 4
	frame := make([]byte, n+plen)
	copy(frame, header[:n])
	for i, b := range payload {
		frame[n+i] = b ^ mask[i%4]
	}
	if err := c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	_, err := c.conn.Write(frame)
	return err
}

func (c *wsConn) ReadMessage(ctx context.Context) ([]byte, error) {
	var message []byte
	started := false
	for {
		if deadline, ok := ctx.Deadline(); ok {
			_ = c.conn.SetReadDeadline(deadline)
		} else {
			_ = c.conn.SetReadDeadline(time.Now().Add(50 * time.Second))
		}
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case wsOpcodePing:
			if err := c.writeFrame(wsOpcodePong, payload); err != nil {
				return nil, err
			}
			continue
		case wsOpcodePong:
			continue
		case wsOpcodeClose:
			return nil, io.EOF
		case wsOpcodeText:
			if started {
				return nil, errors.New("unexpected websocket text frame during fragmented message")
			}
			started = true
			message = append(message, payload...)
		case wsOpcodeContinuation:
			if !started {
				return nil, errors.New("unexpected websocket continuation frame")
			}
			message = append(message, payload...)
		default:
			continue
		}
		if len(message) > wsMaxMessageSize {
			return nil, errors.New("websocket message too large")
		}
		if fin {
			return message, nil
		}
	}
}

func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.reader, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	plen := uint64(h[1] & 0x7F)
	switch plen {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.reader, b[:]); err != nil {
			return false, 0, nil, err
		}
		plen = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.reader, b[:]); err != nil {
			return false, 0, nil, err
		}
		plen = binary.BigEndian.Uint64(b[:])
	}
	if plen > wsMaxMessageSize {
		return false, 0, nil, errors.New("websocket frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.reader, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, int(plen))
	if _, err = io.ReadFull(c.reader, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}
