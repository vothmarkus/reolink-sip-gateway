package ha

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListenerWebSocketTrigger(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/states/binary_sensor.door", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("missing bearer token")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "off"})
	})
	mux.HandleFunc("/core/websocket", func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("hijacking unavailable")
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		key := r.Header.Get("Sec-WebSocket-Key")
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(key))
		if err := rw.Flush(); err != nil {
			t.Error(err)
			return
		}
		if err := serverWriteFrame(conn, wsOpcodeText, []byte(`{"type":"auth_required"}`)); err != nil {
			t.Error(err)
			return
		}
		msg, err := serverReadFrame(rw.Reader)
		if err != nil || !strings.Contains(string(msg), `"access_token":"token"`) {
			t.Errorf("bad auth: %s %v", msg, err)
			return
		}
		_ = serverWriteFrame(conn, wsOpcodeText, []byte(`{"type":"auth_ok"}`))
		msg, err = serverReadFrame(rw.Reader)
		if err != nil || !strings.Contains(string(msg), `"subscribe_trigger"`) || !strings.Contains(string(msg), `"from":"off"`) || !strings.Contains(string(msg), `"to":"on"`) {
			t.Errorf("bad subscription: %s %v", msg, err)
			return
		}
		_ = serverWriteFrame(conn, wsOpcodeText, []byte(`{"id":1,"type":"result","success":true,"result":null}`))
		ev := `{"id":1,"type":"event","event":{"variables":{"trigger":{"to_state":{"state":"on"}}}}}`
		_ = serverWriteFrame(conn, wsOpcodeText, []byte(ev))
		// Keep the connection alive until the test context is canceled.
		time.Sleep(300 * time.Millisecond)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan struct{}, 1)
	l := &Listener{
		WSURL:        "ws" + strings.TrimPrefix(srv.URL, "http") + "/core/websocket",
		RESTBaseURL:  srv.URL,
		Token:        "token",
		EntityID:     "binary_sensor.door",
		PollInterval: 20 * time.Millisecond,
	}
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, ch) }()
	select {
	case <-ch:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("no websocket visitor trigger")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("listener did not stop")
	}
}

func TestListenerWebSocketTriggerDoesNotRequireREST(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/states/binary_sensor.door", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/core/websocket", func(w http.ResponseWriter, r *http.Request) {
		hj := w.(http.Hijacker)
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		_ = rw.Flush()
		_ = serverWriteFrame(conn, wsOpcodeText, []byte(`{"type":"auth_required"}`))
		if _, err := serverReadFrame(rw.Reader); err != nil {
			return
		}
		_ = serverWriteFrame(conn, wsOpcodeText, []byte(`{"type":"auth_ok"}`))
		if _, err := serverReadFrame(rw.Reader); err != nil {
			return
		}
		_ = serverWriteFrame(conn, wsOpcodeText, []byte(`{"id":1,"type":"result","success":true}`))
		_ = serverWriteFrame(conn, wsOpcodeText, []byte(`{"id":1,"type":"event","event":{"variables":{"trigger":{"to_state":{"state":"on"}}}}}`))
		time.Sleep(150 * time.Millisecond)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan struct{}, 1)
	l := &Listener{WSURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/core/websocket", RESTBaseURL: srv.URL, Token: "token", EntityID: "binary_sensor.door"}
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, ch) }()
	select {
	case <-ch:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("websocket event was blocked by REST failure")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("listener did not stop")
	}
}

func serverWriteFrame(w io.Writer, opcode byte, payload []byte) error {
	h := []byte{0x80 | opcode, 0}
	switch {
	case len(payload) < 126:
		h[1] = byte(len(payload))
	case len(payload) <= 0xFFFF:
		h[1] = 126
		h = append(h, byte(len(payload)>>8), byte(len(payload)))
	default:
		h[1] = 127
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(len(payload)))
		h = append(h, b[:]...)
	}
	if _, err := w.Write(h); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func serverReadFrame(r *bufio.Reader) ([]byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if h[1]&0x80 != 0 {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, int(n))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	if h[1]&0x80 != 0 {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, nil
}
