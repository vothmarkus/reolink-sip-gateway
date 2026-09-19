package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/standalone"
	"github.com/vothmarkus/reolink-sip-gateway/internal/status"
)

func TestStandaloneRuntimeWithoutSupervisorStartsAndStops(t *testing.T) {
	t.Setenv("SUPERVISOR_TOKEN", "")
	s := standalone.Defaults()
	s.Reolink.Host = "192.0.2.50"
	s.SIP.Registrar = "192.0.2.1"
	s.Trigger.Source = "manual"
	s.LiveImage.Enabled = false
	cfg, err := s.Runtime(false)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	published, done := make(chan http.Handler, 1), make(chan error, 1)
	go func() {
		done <- runGateway(ctx, cfg, gatewayRuntimeOptions{
			Publish: func(handler http.Handler) { published <- handler },
			UIAuth:  func(handler http.Handler) http.Handler { return handler },
		})
	}()
	var handler http.Handler
	select {
	case handler = <-published:
	case err := <-done:
		t.Fatalf("runtime failed before publishing status: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not publish status")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/status", nil))
		var snapshot status.Snapshot
		if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.State == "idle" {
			if snapshot.HAConnected || !snapshot.TriggerConnected || snapshot.TriggerSource != "manual" || !snapshot.DryRun {
				t.Fatal("standalone status does not match configured trigger and mode")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime did not become ready: %s", snapshot.State)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "integration-api-token")); err != nil {
		t.Fatalf("identity was not created in selected state directory: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not release its background workers")
	}
}
