package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func visitorRegistryServer(t *testing.T, entries []map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
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
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(r.Header.Get("Sec-WebSocket-Key")))
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
		var request struct {
			Type string `json:"type"`
		}
		if err != nil || json.Unmarshal(msg, &request) != nil || request.Type != "config/entity_registry/list_for_display" {
			t.Errorf("bad entity registry request: %s %v", msg, err)
			return
		}
		result, _ := json.Marshal(map[string]any{"entities": entries})
		response, _ := json.Marshal(map[string]any{"id": 1, "type": "result", "success": true, "result": json.RawMessage(result)})
		_ = serverWriteFrame(conn, wsOpcodeText, response)
		time.Sleep(20 * time.Millisecond)
	})
	return httptest.NewServer(mux)
}

func TestResolveReolinkVisitorEntitySingleEnabled(t *testing.T) {
	srv := visitorRegistryServer(t, []map[string]any{
		{"ei": "binary_sensor.front_door_visitor", "pl": "reolink", "tk": "visitor"},
		{"ei": "binary_sensor.front_door_person", "pl": "reolink", "tk": "person"},
		{"ei": "binary_sensor.other_visitor", "pl": "other", "tk": "visitor"},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := ResolveReolinkVisitorEntity(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/core/websocket", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "binary_sensor.front_door_visitor" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveReolinkVisitorEntityMultiple(t *testing.T) {
	srv := visitorRegistryServer(t, []map[string]any{
		{"ei": "binary_sensor.b_visitor", "pl": "reolink", "tk": "visitor"},
		{"ei": "binary_sensor.a_visitor", "pl": "reolink", "tk": "visitor"},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := ResolveReolinkVisitorEntity(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/core/websocket", "token")
	if err == nil || !strings.Contains(err.Error(), "multiple enabled Reolink visitor binary sensors found: binary_sensor.a_visitor, binary_sensor.b_visitor") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveReolinkVisitorEntityNone(t *testing.T) {
	srv := visitorRegistryServer(t, []map[string]any{
		{"ei": "binary_sensor.front_door_person", "pl": "reolink", "tk": "person"},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := ResolveReolinkVisitorEntity(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/core/websocket", "token")
	if err == nil || !strings.Contains(err.Error(), "no enabled Reolink visitor binary sensor found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveReolinkVisitorEntityLargeRegistryFrame(t *testing.T) {
	srv := visitorRegistryServer(t, []map[string]any{
		{"ei": "sensor.large_metadata", "pl": "test", "en": strings.Repeat("x", 3<<20)},
		{"ei": "binary_sensor.front_door_visitor", "pl": "reolink", "tk": "visitor"},
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := ResolveReolinkVisitorEntity(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/core/websocket", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "binary_sensor.front_door_visitor" {
		t.Fatalf("got %q", got)
	}
}
