package standalone

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "config.json"), dir, 18099, "2.0.0-test", strings.Repeat("a", 43), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func send(t *testing.T, s *Server, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "http://gateway.local"+path, bytes.NewReader(raw))
	r.RemoteAddr = "192.168.1.20:30000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://gateway.local")
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func login(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	w := send(t, s, "POST", "/login", map[string]string{"token": s.adminToken}, nil, "")
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie is not protected")
	}
	return cookie, s.signature("csrf:" + cookie.Value)
}

func TestSetupAvailableWithoutConfigurationAndRequiresAuthentication(t *testing.T) {
	s := testServer(t)
	if w := send(t, s, "GET", "/", nil, nil, ""); w.Code != 200 {
		t.Fatal("setup page unavailable")
	}
	if w := send(t, s, "GET", "/admin/config", nil, nil, ""); w.Code != 401 {
		t.Fatal("configuration not protected")
	}
	if w := send(t, s, "GET", "/admin/export", nil, nil, ""); w.Code != 401 {
		t.Fatal("export not protected")
	}
	if w := send(t, s, "POST", "/login", map[string]string{"token": "wrong"}, nil, ""); w.Code != 401 {
		t.Fatal("bad password accepted")
	}
	cookie, _ := login(t, s)
	if w := send(t, s, "GET", "/admin/config", nil, cookie, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatal("bootstrap state unavailable")
	}
	r := httptest.NewRequest("GET", "http://gateway.local/", nil)
	r.RemoteAddr = "203.0.113.40:443"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("public source accepted")
	}
}

func TestSavePreservesPasswordsRejectsCSRFAndStaleEdits(t *testing.T) {
	s := testServer(t)
	cookie, csrf := login(t, s)
	settings := validSettings()
	request := map[string]any{"config": settings, "revision": s.revision}
	if w := send(t, s, "POST", "/admin/config", request, cookie, ""); w.Code != 403 {
		t.Fatal("write without CSRF accepted")
	}
	if _, err := os.Stat(s.ConfigPath); !os.IsNotExist(err) {
		t.Fatal("CSRF request wrote a file")
	}
	if w := send(t, s, "POST", "/admin/config", request, cookie, csrf); w.Code != 202 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	stat, err := os.Stat(s.ConfigPath)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("configuration file permissions")
	}
	if w := send(t, s, "POST", "/admin/config", request, cookie, csrf); w.Code != 409 {
		t.Fatal("stale revision accepted")
	}
	w := send(t, s, "GET", "/admin/config", nil, cookie, "")
	if strings.Contains(w.Body.String(), "camera-secret") || strings.Contains(w.Body.String(), s.adminToken) {
		t.Fatal("secret leaked in configuration response")
	}
	settings.Reolink.Password = ""
	settings.Reolink.Channel = 3
	request = map[string]any{"config": settings, "revision": s.revision}
	if w = send(t, s, "POST", "/admin/config", request, cookie, csrf); w.Code != 202 {
		t.Fatalf("masked save: %d %s", w.Code, w.Body.String())
	}
	raw, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := DecodeSettings(raw)
	if err != nil || saved.Reolink.Password != "camera-secret" || saved.Reolink.Channel != 3 {
		t.Fatal("password or channel not preserved")
	}
	old, err := os.ReadFile(s.ConfigPath + ".previous")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := DecodeSettings(old)
	if err != nil || previous.Reolink.Channel != 1 {
		t.Fatal("previous configuration not preserved")
	}
	request = map[string]any{"config": settings, "revision": s.revision, "clear_secrets": []string{"reolink"}}
	if w = send(t, s, "POST", "/admin/config", request, cookie, csrf); w.Code != 400 {
		t.Fatal("invalid configuration with cleared required credential accepted")
	}
	after, _ := os.ReadFile(s.ConfigPath)
	if !bytes.Equal(raw, after) {
		t.Fatal("invalid save modified working config")
	}
	if err := os.WriteFile(s.ConfigPath, append(raw, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	request = map[string]any{"config": settings, "revision": s.revision}
	if w = send(t, s, "POST", "/admin/config", request, cookie, csrf); w.Code != 409 {
		t.Fatal("external file change overwritten")
	}
}

func TestCrossOriginRequestCannotSaveEvenWithSessionAndCSRF(t *testing.T) {
	s := testServer(t)
	cookie, csrf := login(t, s)
	raw, _ := json.Marshal(map[string]any{"config": validSettings(), "revision": s.revision})
	r := httptest.NewRequest("POST", "http://gateway.local/admin/config", bytes.NewReader(raw))
	r.RemoteAddr = "192.168.1.20:3000"
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://other.local")
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin write accepted")
	}
}

func TestInvalidConfigurationKeepsSetupReachable(t *testing.T) {
	s := testServer(t)
	if err := os.WriteFile(s.ConfigPath, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.ConfigPath, s.DataDir, 18099, "test", s.APIToken, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := login(t, reopened)
	w := send(t, reopened, "GET", "/admin/config", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) || reopened.lastError == "" {
		t.Fatal("invalid file prevented setup")
	}
}

func TestRuntimeReloadWaitsForCleanupAndKeepsWebAccess(t *testing.T) {
	s := testServer(t)
	cookie, csrf := login(t, s)
	started := make(chan int, 3)
	stopping := make(chan struct{}, 1)
	release := make(chan struct{})
	var generation atomic.Int32
	s.Run = func(ctx context.Context, cfg config.Config, publish func(http.Handler)) error {
		n := int(generation.Add(1))
		publish(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "runtime") }))
		started <- n
		<-ctx.Done()
		if n == 1 {
			stopping <- struct{}{}
			<-release
		}
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Manage(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("manager did not stop")
		}
	}()
	request := map[string]any{"config": validSettings(), "revision": s.revision}
	if w := send(t, s, "POST", "/admin/config", request, cookie, csrf); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not start")
	}
	request["revision"] = s.revision
	if w := send(t, s, "POST", "/admin/config", request, cookie, csrf); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	select {
	case <-stopping:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime was not stopped")
	}
	if generation.Load() != 1 {
		t.Fatal("overlapping runtime generations")
	}
	if w := send(t, s, "GET", "/admin/config", nil, cookie, ""); w.Code != 200 {
		t.Fatal("setup blocked during cleanup")
	}
	close(release)
	select {
	case n := <-started:
		if n != 2 {
			t.Fatal("unexpected generation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new runtime did not start")
	}
}
