package mobilebridge

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLANImageListenerCannotReachControlOrSetup(t *testing.T) {
	const path = "/fritzfon/test-secret.jpg"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := imageOnlyHandler(next, path)
	for _, target := range []string{"/", "/health", "/api/v1/status", "/api/v1/calls/test", "/fritzfon/wrong.jpg", path + "/", "/fritzfon/../api/v1/status", path} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		want := http.StatusNotFound
		if target == path {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Fatalf("%s: %d want %d", target, w.Code, want)
		}
	}
}

func TestAndroidImagePortLifecycleAndBindFailure(t *testing.T) {
	s := androidSettings()
	s.Trigger.Source = "manual"
	s.LiveImage.Enabled = true
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.LiveImage.Port = occupied.Addr().(*net.TCPAddr).Port
	raw := encodeSettings(t, s)
	if g, err := Start(raw, t.TempDir(), nil, nil); err == nil {
		g.Stop()
		t.Fatal("occupied image port was ignored")
	}
	occupied.Close()
	g, err := Start(raw, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal("failed start retained singleton:", err)
	}
	defer g.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err = g.StatusJSON()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(raw, g.imagePath) || strings.Contains(raw, g.token) {
		t.Fatal("status leaks capability token")
	}
	endpoint := "http://127.0.0.1:" + strings.Split(g.imageListener.Addr().String(), ":")[1]
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/api/v1/status", "/api/v1/calls/test", "/"} {
		resp, err := client.Get(endpoint + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("LAN route %s exposed: %d", path, resp.StatusCode)
		}
	}
	address := g.imageListener.Addr().String()
	if err := g.Stop(); err != nil {
		t.Fatal(err)
	}
	rebound, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal("image listener leaked:", err)
	}
	rebound.Close()
}
