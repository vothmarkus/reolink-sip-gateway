package mobilebridge

import (
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/standalone"
)

func androidSettings() standalone.Settings {
	s := standalone.Defaults()
	s.Reolink.Host, s.Reolink.Password, s.Reolink.Mode = "192.0.2.50", "camera-secret", "direct"
	s.SIP.Registrar = "192.0.2.1"
	s.Audio = standalone.AudioSettings{}
	s.LiveImage.Enabled = false
	return s
}
func encodeSettings(t *testing.T, s standalone.Settings) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAndroidDirectConfigUsesCameraChannelAndBaichuan(t *testing.T) {
	s := androidSettings()
	s.Reolink.Channel = 17 // Previously saved NVR selection must not address channel 16.
	raw := encodeSettings(t, s)
	if err := ValidateConfig(raw); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.Runtime(false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NVRChannel != 0 || cfg.ReceiveMode() != "baichuan" || cfg.ReolinkMode != "direct" || cfg.TriggerSource != "baichuan" {
		t.Fatalf("unexpected direct-camera configuration: mode=%s channel=%d receive=%s trigger=%s", cfg.ReolinkMode, cfg.NVRChannel, cfg.ReceiveMode(), cfg.TriggerSource)
	}
	s.Reolink.Mode = "nvr"
	cfg, err = s.Runtime(false)
	if err != nil || cfg.NVRChannel != 16 {
		t.Fatalf("NVR mapping changed: %d, %v", cfg.NVRChannel, err)
	}
}

func TestAndroidValidationIncludesSharedRules(t *testing.T) {
	tests := map[string]func(*standalone.Settings){
		"RTSP requires Linux":               func(s *standalone.Settings) { s.Reolink.Mode = "standalone" },
		"auto may select FFmpeg":            func(s *standalone.Settings) { s.Reolink.Mode = "auto" },
		"registrar auto unavailable":        func(s *standalone.Settings) { s.SIP.Registrar = "auto" },
		"filters need AEC":                  func(s *standalone.Settings) { s.Audio.HighPass = true },
		"privileged image port":             func(s *standalone.Settings) { s.LiveImage.Port = 80 },
		"camera host required":              func(s *standalone.Settings) { s.Reolink.Host = "" },
		"credentials for active SIP":        func(s *standalone.Settings) { s.Diagnostics.DryRun = false },
		"invalid SIP port":                  func(s *standalone.Settings) { s.SIP.LocalPort = 0 },
		"caller filter cannot mix wildcard": func(s *standalone.Settings) { s.Call.Incoming = true; s.Call.AllowedCallers = []string{"*", "**610"} },
		"parallel needs unique port": func(s *standalone.Settings) {
			s.SIP.ParallelEnabled = true
			s.SIP.ParallelLocalPort = s.SIP.LocalPort
			s.CallRoutes[0].MobileNumber1 = "**611"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := androidSettings()
			mutate(&s)
			if ValidateConfig(encodeSettings(t, s)) == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestAndroidRuntimeStopsAndReleasesLoopbackAndSingleton(t *testing.T) {
	s := androidSettings()
	s.Trigger.Source = "manual" // No camera or SIP network traffic in this test.
	raw := encodeSettings(t, s)
	for i := 0; i < 2; i++ {
		g, err := Start(raw, t.TempDir(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = g.Stop() })
		if _, err := Start(raw, t.TempDir(), nil, nil); err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("second concurrent runtime accepted: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			status, err := g.StatusJSON()
			if err == nil && strings.Contains(status, `"dry_run":true`) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("status API unavailable: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := g.Stop(); err != nil {
			t.Fatal(err)
		}
		if g.IsRunning() {
			t.Fatal("runtime still marked running")
		}
		if err := g.Stop(); err != nil {
			t.Fatal("stop is not idempotent:", err)
		}
		u, _ := url.Parse(g.baseURL)
		conn, err := net.DialTimeout("tcp", u.Host, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Fatal("loopback port remained open after stop")
		}
	}
}

func TestAndroidAcceptsAECAndLiveImages(t *testing.T) {
	s := androidSettings()
	s.Audio = standalone.AudioSettings{AEC: true, HighPass: true, NoiseSuppression: true}
	s.LiveImage.Enabled = true
	if err := ValidateConfig(encodeSettings(t, s)); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.Runtime(false)
	if err != nil || !cfg.EchoCancellationEnabled || !cfg.FritzFonLiveImageEnabled || cfg.StatusPort != 18099 {
		t.Fatalf("feature configuration lost: %+v %v", cfg, err)
	}
}
