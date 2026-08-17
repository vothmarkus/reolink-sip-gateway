package status

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testCommands struct {
	testCall func(context.Context) error
	hangup   func(context.Context) error
}

func (c testCommands) StartTestCall(ctx context.Context) error {
	if c.testCall == nil {
		return nil
	}
	return c.testCall(ctx)
}

func (c testCommands) Hangup(ctx context.Context) error {
	if c.hangup == nil {
		return nil
	}
	return c.hangup(ctx)
}

func TestAPIV1RequiresBearerToken(t *testing.T) {
	store, handler := newTestAPI(t, testCommands{})
	_ = store
	for _, token := range []string{"", "wrong"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
		req.RemoteAddr = "192.168.1.20:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d, want 401", token, res.Code)
		}
	}

	req := authenticatedRequest(http.MethodGet, "/api/v1/info")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("valid token status = %d body=%s", res.Code, res.Body.String())
	}
	var info APIInfo
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.APIVersion != APIVersion || info.GatewayVersion != "0.9.0" || info.InstanceID != "instance-1" {
		t.Fatalf("unexpected info: %#v", info)
	}
}

func TestAPIV1RejectsPublicRemoteEvenWithToken(t *testing.T) {
	_, handler := newTestAPI(t, testCommands{})
	req := authenticatedRequest(http.MethodGet, "/api/v1/status")
	req.RemoteAddr = "203.0.113.10:1234"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
}

func TestAPIV1StatusMapping(t *testing.T) {
	store, handler := newTestAPI(t, testCommands{})
	started := time.Now().Add(-time.Minute).Round(time.Second)
	store.Update(func(snapshot *Snapshot) {
		snapshot.State = "active"
		snapshot.SIPRegistered = true
		snapshot.CurrentCallDirection = "incoming"
		snapshot.LastCallDirection = "incoming"
		snapshot.CurrentCallerNumber = "01631416518"
		snapshot.LastCallerNumber = "01631416518"
		snapshot.LastCallStarted = started
		snapshot.ActiveCodec = "pcma"
	})
	req := authenticatedRequest(http.MethodGet, "/api/v1/status")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var status APIStatus
	if err := json.Unmarshal(res.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Call.Active || status.Call.State != "active" || status.Call.CallerNumber != "01631416518" || !status.Controls.HangupAvailable {
		t.Fatalf("unexpected status mapping: %#v", status)
	}
	if status.Controls.TestCallAvailable {
		t.Fatal("test call must not be available during a call")
	}
}

func TestAPIV1Commands(t *testing.T) {
	testCalled := false
	hangupCalled := false
	_, handler := newTestAPI(t, testCommands{
		testCall: func(context.Context) error { testCalled = true; return nil },
		hangup:   func(context.Context) error { hangupCalled = true; return nil },
	})
	for _, path := range []string{"/api/v1/calls/test", "/api/v1/calls/hangup"} {
		req := authenticatedRequest(http.MethodPost, path)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d body=%s", path, res.Code, res.Body.String())
		}
	}
	if !testCalled || !hangupCalled {
		t.Fatalf("callbacks test=%t hangup=%t", testCalled, hangupCalled)
	}
}

func TestAPIV1CommandErrors(t *testing.T) {
	_, handler := newTestAPI(t, testCommands{
		testCall: func(context.Context) error { return ErrCallBusy },
		hangup:   func(context.Context) error { return ErrNoActiveCall },
	})
	tests := []struct {
		path string
		want int
	}{
		{"/api/v1/calls/test", http.StatusConflict},
		{"/api/v1/calls/hangup", http.StatusNoContent},
	}
	for _, tt := range tests {
		req := authenticatedRequest(http.MethodPost, tt.path)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tt.want {
			t.Fatalf("%s status = %d, want %d body=%s", tt.path, res.Code, tt.want, res.Body.String())
		}
	}
}

func TestAPIV1EventsStartsWithCompleteSnapshot(t *testing.T) {
	store, handler := newTestAPI(t, testCommands{})
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected event response: %s content-type=%q", response.Status, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	var data string
	for data == "" {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
	var status APIStatus
	if err := json.Unmarshal([]byte(data), &status); err != nil {
		t.Fatal(err)
	}
	if status.Revision != store.Get().Revision || status.APIVersion != APIVersion {
		t.Fatalf("unexpected event status: %#v", status)
	}
}

func TestWriteCommandErrorDoesNotExposeInternalDetails(t *testing.T) {
	res := httptest.NewRecorder()
	writeCommandError(res, errors.New("password=secret"))
	if res.Code != http.StatusInternalServerError || strings.Contains(res.Body.String(), "secret") {
		t.Fatalf("unsafe error response: status=%d body=%s", res.Code, res.Body.String())
	}
}

func newTestAPI(t *testing.T, commands CommandHandler) (*Store, http.Handler) {
	t.Helper()
	store := New("0.9.0")
	mux := http.NewServeMux()
	store.registerAPIRoutes(mux, ServerOptions{Token: "test-token", InstanceID: "instance-1", Commands: commands})
	return store, mux
}

func authenticatedRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.168.1.20:1234"
	req.Header.Set("Authorization", "Bearer test-token")
	return req
}
