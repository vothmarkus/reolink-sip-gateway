package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsAreUserFriendlyV050(t *testing.T) {
	cfg := Defaults()
	if cfg.ReolinkUsername != "admin" || cfg.ReolinkMode != "auto" || cfg.ReolinkRTSPPort != 554 || cfg.BaichuanPort != 9000 || cfg.NVRChannel != 1 {
		t.Fatalf("unexpected Reolink defaults: %#v", cfg)
	}
	if !cfg.EchoCancellationEnabled || cfg.EchoCancellationSearchWindowMS != 300 || cfg.AECInitialDelayMS != 1450 || cfg.AECMinDelayMS != 1150 || cfg.AECMaxDelayMS != 1750 {
		t.Fatalf("unexpected AEC defaults: %#v", cfg)
	}
	if !cfg.WebRTCHighPassFilterEnabled || !cfg.WebRTCNoiseSuppressionEnabled || WebRTCNoiseSuppressionLevel != "moderate" {
		t.Fatalf("unexpected WebRTC defaults: %#v", cfg)
	}
	if cfg.HAPollInterval() != time.Second || cfg.FFmpegPath() != "/usr/bin/ffmpeg" || BaichuanReceiveStream != "sub" {
		t.Fatalf("unexpected fixed runtime defaults")
	}
}

func TestDryRunDoesNotRequireCredentials(t *testing.T) {
	cfg := Defaults()
	cfg.ReolinkUsername, cfg.ReolinkPassword = "", ""
	cfg.SIPUsername, cfg.SIPPassword = "", ""
	cfg.SIPDestination = ""
	cfg.DryRun = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dry run should validate: %v", err)
	}
}

func TestLiveModeRequiresCredentials(t *testing.T) {
	cfg := Defaults()
	cfg.DryRun = false
	cfg.ReolinkUsername, cfg.ReolinkPassword = "", ""
	cfg.SIPUsername, cfg.SIPPassword, cfg.SIPDestination = "", "", ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation failure")
	}
	for _, want := range []string{"reolink_username", "reolink_password", "sip_username", "sip_password", "sip_destination"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %s in %v", want, err)
		}
	}
}

func TestReolinkModeValidation(t *testing.T) {
	for _, mode := range []string{"auto", "nvr", "standalone"} {
		cfg := Defaults()
		cfg.ReolinkMode = mode
		if err := cfg.Validate(); err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
	cfg := Defaults()
	cfg.ReolinkMode = "camera-ish"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reolink_mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAECSearchWindowValidationAndDelayBounds(t *testing.T) {
	for _, bad := range []int{49, 1001} {
		cfg := Defaults()
		cfg.EchoCancellationSearchWindowMS = bad
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "echo_cancellation_search_window_ms") {
			t.Fatalf("window %d: %v", bad, err)
		}
	}
	cfg := Defaults()
	cfg.EchoCancellationSearchWindowMS = 300
	cfg.SetAECDelay(1430)
	if cfg.AECInitialDelayMS != 1430 || cfg.AECMinDelayMS != 1130 || cfg.AECMaxDelayMS != 1730 {
		t.Fatalf("unexpected delay range: %#v", cfg)
	}
	cfg.SetAECDelay(100)
	if cfg.AECMinDelayMS != 0 || cfg.AECMaxDelayMS != 400 {
		t.Fatalf("low clamp failed: %d..%d", cfg.AECMinDelayMS, cfg.AECMaxDelayMS)
	}
	cfg.SetAECDelay(2900)
	if cfg.AECMinDelayMS != 2600 || cfg.AECMaxDelayMS != 3000 {
		t.Fatalf("high clamp failed: %d..%d", cfg.AECMinDelayMS, cfg.AECMaxDelayMS)
	}
}

func TestResolvedModeDeterminesWholeMediaProfile(t *testing.T) {
	cfg := Defaults().WithResolvedReolinkMode("nvr")
	if cfg.EffectiveReolinkMode() != "nvr" || cfg.ReceiveMode() != "baichuan" {
		t.Fatalf("NVR resolution failed")
	}
	cfg = Defaults().WithResolvedReolinkMode("standalone")
	if cfg.EffectiveReolinkMode() != "standalone" || cfg.ReceiveMode() != "rtsp" {
		t.Fatalf("standalone resolution failed")
	}
}

func TestDebugLevelControlsDiagnostics(t *testing.T) {
	cfg := Defaults()
	cfg.LogLevel = "info"
	if cfg.DebugEnabled() {
		t.Fatal("info must not enable diagnostics")
	}
	cfg.LogLevel = "debug"
	if !cfg.DebugEnabled() {
		t.Fatal("debug must enable diagnostics")
	}
}

func TestFFmpegPathRuntimeOverrideIsNotAUserOption(t *testing.T) {
	cfg := Defaults()
	cfg.FFmpegBinaryPath = "/tmp/fake-ffmpeg"
	if got := cfg.FFmpegPath(); got != "/tmp/fake-ffmpeg" {
		t.Fatalf("got %q", got)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "fake-ffmpeg") || strings.Contains(string(b), "FFmpegBinaryPath") {
		t.Fatalf("runtime path leaked into JSON: %s", b)
	}
}

func TestLoadNormalizesAndMigratesV04Aliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{
      "visitor_entity":"binary_sensor.test_visitor",
      "reolink_host":"192.0.2.10",
      "reolink_stream_path":"Preview_01_sub",
      "connection_mode":"nvr",
      "baichuan_channel":3,
      "sip_registrar":"192.0.2.20",
      "sip_codec_preference":"PCMA",
      "log_level":"DEBUG",
      "dry_run":true,
      "echo_cancellation_delay_ms":9999,
      "latency_test":true,
      "debug_sip":true
    }`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReolinkStreamPath != "/Preview_01_sub" || cfg.ReolinkMode != "nvr" || cfg.NVRChannel != 3 {
		t.Fatalf("migration failed: %#v", cfg)
	}
	if cfg.SIPCodecPreference != "pcma" || cfg.LogLevel != "debug" {
		t.Fatalf("normalization failed: %#v", cfg)
	}
	if cfg.AECInitialDelayMS != DefaultAECInitialDelayMS {
		t.Fatalf("retired delay option must be ignored, got %d", cfg.AECInitialDelayMS)
	}
}

func TestNewKeysOverrideLegacyAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{"reolink_mode":"standalone","connection_mode":"nvr","nvr_channel":7,"baichuan_channel":3,"dry_run":true}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReolinkMode != "standalone" || cfg.NVRChannel != 7 {
		t.Fatalf("new keys did not win: %#v", cfg)
	}
}

func TestHostAndInjectionValidation(t *testing.T) {
	cfg := Defaults()
	cfg.ReolinkHost = "rtsp://camera.example/path"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reolink_host") {
		t.Fatalf("expected host error, got %v", err)
	}
	cfg = Defaults()
	cfg.SIPRegistrar = "2001:db8::1"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "IPv6") {
		t.Fatalf("expected IPv6 SIP error, got %v", err)
	}
	cfg = Defaults()
	cfg.SIPDisplayName = "Door\r\nInjected: x"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "sip_display_name") {
		t.Fatalf("expected CR/LF error, got %v", err)
	}
}

func TestLegacyNonDefaultValuesReplaceMaterializedV050DefaultsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{"reolink_mode":"auto","connection_mode":"nvr","nvr_channel":1,"baichuan_channel":4,"dry_run":true}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReolinkMode != "nvr" || cfg.NVRChannel != 4 {
		t.Fatalf("legacy non-default values not preserved: mode=%q channel=%d", cfg.ReolinkMode, cfg.NVRChannel)
	}
}
