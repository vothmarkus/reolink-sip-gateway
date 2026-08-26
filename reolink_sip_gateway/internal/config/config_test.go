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
	if cfg.IncomingCallsEnabled {
		t.Fatal("incoming calls must remain opt-in for upgrades and fresh installs")
	}
	if len(cfg.IncomingAllowedCallers) != 1 || cfg.IncomingAllowedCallers[0] != "*" {
		t.Fatalf("unexpected incoming caller default: %#v", cfg.IncomingAllowedCallers)
	}
	if !cfg.IncomingConnectionToneEnabled || cfg.RTPInactivityTimeout() != 15*time.Second {
		t.Fatalf("unexpected incoming-call safety defaults: %#v", cfg)
	}
	if !cfg.DoorCallEnabled || cfg.ParallelCallEnabled || cfg.ParallelLocalPort != 5071 {
		t.Fatalf("unexpected parallel-call defaults: %#v", cfg)
	}
	if len(cfg.CallRoutes) != 1 || cfg.CallRoutes[0].ID != DefaultRouteID || cfg.CallRoutes[0].Name != "Standardroute" || cfg.CallRoutes[0].DoorbellNumber != "11" {
		t.Fatalf("unexpected default route: %#v", cfg.CallRoutes)
	}
}

func TestParallelCallValidationAndNormalization(t *testing.T) {
	cfg := Defaults()
	cfg.DryRun = false
	cfg.ReolinkPassword = "camera-secret"
	cfg.SIPUsername = "door"
	cfg.SIPPassword = "door-secret"
	cfg.ParallelCallEnabled = true
	cfg.ParallelUsername = "mobile"
	cfg.ParallelPassword = "mobile-secret"
	cfg.CallRoutes[0].MobileNumber1 = "0163"
	cfg.CallRoutes[0].MobileNumber2 = "0176"
	cfg.CallRoutes[0].MobileNumber3 = "0151"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid parallel configuration failed: %v", err)
	}

	for name, mutate := range map[string]func(*Config){
		"missing destinations": func(c *Config) {
			c.CallRoutes[0].MobileNumber1 = ""
			c.CallRoutes[0].MobileNumber2 = ""
			c.CallRoutes[0].MobileNumber3 = ""
		},
		"duplicate destination": func(c *Config) { c.CallRoutes[0].MobileNumber2 = "01 63" },
		"same local port":       func(c *Config) { c.ParallelLocalPort = c.SIPLocalPort },
		"missing username":      func(c *Config) { c.ParallelUsername = "" },
		"missing password":      func(c *Config) { c.ParallelPassword = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cfg
			candidate.CallRoutes = append([]CallRoute(nil), cfg.CallRoutes...)
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{"parallel_call_enabled":true,"call_routes":[{"id":"default","name":"Standardroute","visitor_entity":"binary_sensor.test_visitor","doorbell_number":"11","mobile_number_1":" 0163 ","mobile_number_2":" 0176 ","mobile_number_3":""}],"dry_run":true}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.CallRoutes[0].MobileNumbers(); len(got) != 2 || got[0] != "0163" || got[1] != "0176" {
		t.Fatalf("normalized destinations=%#v", got)
	}
}

func TestMobileOnlyLiveConfiguration(t *testing.T) {
	cfg := Defaults()
	cfg.DryRun = false
	cfg.ReolinkPassword = "camera-secret"
	cfg.DoorCallEnabled = false
	cfg.SIPUsername = ""
	cfg.SIPPassword = ""
	cfg.ParallelCallEnabled = true
	cfg.ParallelUsername = "mobile"
	cfg.ParallelPassword = "mobile-secret"
	cfg.CallRoutes[0].DoorbellNumber = ""
	cfg.CallRoutes[0].MobileNumber1 = "0163"
	cfg.CallRoutes[0].MobileNumber2 = "0176"
	cfg.CallRoutes[0].MobileNumber3 = "0151"
	// No door socket is opened, so its otherwise unused port may be identical.
	cfg.ParallelLocalPort = cfg.SIPLocalPort
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid mobile-only configuration failed: %v", err)
	}

	cfg.ParallelCallEnabled = false
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("live configuration without a SIP account should fail: %v", err)
	}
}

func TestRoutingMatrixValidationAndResolution(t *testing.T) {
	cfg := validRoutingConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid routing matrix failed: %v", err)
	}
	routes := cfg.ResolvedCallRoutes()
	if len(routes) != 2 || routes[0].ID != "wohnung_1" || routes[0].DoorbellNumber != "11" || len(routes[0].MobileTargets) != 2 || routes[0].MobileTargets[1].Destination != "0176" {
		t.Fatalf("resolved routes=%#v", routes)
	}
	if cfg.MaxMobileTargetsPerRoute() != 2 {
		t.Fatalf("maximum mobile targets=%d", cfg.MaxMobileTargetsPerRoute())
	}
	if route, ok := cfg.FindCallRoute("wohnung_2"); !ok || route.Name != "Wohnung 2" {
		t.Fatalf("route=%#v ok=%t", route, ok)
	}
}

func TestRoutingMatrixRejectsAmbiguousOrBrokenEntries(t *testing.T) {
	tests := map[string]func(*Config){
		"duplicate route id":        func(c *Config) { c.CallRoutes[1].ID = c.CallRoutes[0].ID },
		"duplicate entity":          func(c *Config) { c.CallRoutes[1].VisitorEntity = c.CallRoutes[0].VisitorEntity },
		"duplicate doorbell number": func(c *Config) { c.CallRoutes[1].DoorbellNumber = c.CallRoutes[0].DoorbellNumber },
		"duplicate mobile number":   func(c *Config) { c.CallRoutes[0].MobileNumber2 = "01 63" },
		"invalid route id":          func(c *Config) { c.CallRoutes[0].ID = "Wohnung-1" },
		"missing route path": func(c *Config) {
			c.CallRoutes[1].DoorbellNumber = ""
			c.CallRoutes[1].MobileNumber1 = ""
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validRoutingConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestLegacyConfigurationMigratesToDefaultRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{"visitor_entity":"binary_sensor.legacy_visitor","sip_destination":"12","parallel_destinations":["0163","0176"],"dry_run":true}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	routes := cfg.ResolvedCallRoutes()
	if len(routes) != 1 || routes[0].ID != DefaultRouteID || routes[0].Name != "Standardroute" || routes[0].VisitorEntity != "binary_sensor.legacy_visitor" || routes[0].DoorbellNumber != "12" || len(routes[0].MobileTargets) != 2 || routes[0].MobileTargets[1].Destination != "0176" {
		t.Fatalf("legacy route=%#v", routes)
	}
}

func TestLoadNormalizesRoutingMatrixWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{
		"dry_run": true,
		"call_routes": [{"id":" wohnung_1 ","name":" Wohnung 1 ","visitor_entity":" binary_sensor.klingel_1 ","doorbell_number":" 11 ","mobile_number_1":" 0163 ","mobile_number_2":"","mobile_number_3":""}]
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	route := cfg.ResolvedCallRoutes()[0]
	if route.ID != "wohnung_1" || route.Name != "Wohnung 1" || route.DoorbellNumber != "11" || route.MobileTargets[0].Destination != "0163" {
		t.Fatalf("normalized route=%#v", route)
	}
}

func TestLoadMigratesNamedTargetsAndDirectValuesFromV121(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{
		"dry_run": true,
		"mobile_targets": [{"id":"markus","name":"Markus","destination":"0163"}],
		"call_routes": [{"id":"wohnung_1","name":"Wohnung 1","visitor_entity":"binary_sensor.klingel_1","doorbell_number":"11","mobile_target_1":"markus","mobile_target_2":"0176","mobile_target_3":""}]
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	route := cfg.ResolvedCallRoutes()[0]
	if len(route.MobileTargets) != 2 || route.MobileTargets[0].Destination != "0163" || route.MobileTargets[1].Destination != "0176" {
		t.Fatalf("migrated route=%#v", route)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"mobile_targets", "mobile_target_1", "parallel_destinations", "sip_destination"} {
		if strings.Contains(string(encoded), retired) {
			t.Fatalf("retired routing field %q leaked into migrated config: %s", retired, encoded)
		}
	}
}

func validRoutingConfig() Config {
	cfg := Defaults()
	cfg.DryRun = false
	cfg.ReolinkPassword = "camera-secret"
	cfg.SIPUsername = "door"
	cfg.SIPPassword = "door-secret"
	cfg.ParallelCallEnabled = true
	cfg.ParallelUsername = "mobile"
	cfg.ParallelPassword = "mobile-secret"
	cfg.CallRoutes = []CallRoute{
		{ID: "wohnung_1", Name: "Wohnung 1", VisitorEntity: "binary_sensor.klingel_1", DoorbellNumber: "11", MobileNumber1: "0163", MobileNumber2: "0176"},
		{ID: "wohnung_2", Name: "Wohnung 2", VisitorEntity: "binary_sensor.klingel_2", DoorbellNumber: "12", MobileNumber1: "0163"},
	}
	return cfg
}

func TestIncomingCallerAndRTPWatchdogValidation(t *testing.T) {
	cfg := Defaults()
	cfg.IncomingCallsEnabled = true
	cfg.IncomingAllowedCallers = nil
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "incoming_allowed_callers") {
		t.Fatalf("empty enabled whitelist should fail: %v", err)
	}

	cfg = Defaults()
	cfg.IncomingAllowedCallers = []string{"*", "123"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("mixed wildcard should fail: %v", err)
	}

	cfg = Defaults()
	cfg.IncomingCallsEnabled = true
	cfg.IncomingAllowedCallers = []string{"   "}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("blank caller should fail: %v", err)
	}

	cfg = Defaults()
	cfg.IncomingAllowedCallers = []string{" * ", "123"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("trimmed mixed wildcard should fail: %v", err)
	}

	for _, timeout := range []int{4, 121} {
		cfg = Defaults()
		cfg.RTPInactivityTimeoutSeconds = timeout
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "rtp_inactivity_timeout_seconds") {
			t.Fatalf("watchdog timeout %d should fail: %v", timeout, err)
		}
	}
}

func TestDryRunDoesNotRequireCredentials(t *testing.T) {
	cfg := Defaults()
	cfg.ReolinkUsername, cfg.ReolinkPassword = "", ""
	cfg.SIPUsername, cfg.SIPPassword = "", ""
	cfg.DoorCallEnabled = false
	cfg.ParallelCallEnabled = false
	cfg.DryRun = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dry run should validate: %v", err)
	}
}

func TestLiveModeRequiresCredentials(t *testing.T) {
	cfg := Defaults()
	cfg.DryRun = false
	cfg.ReolinkUsername, cfg.ReolinkPassword = "", ""
	cfg.SIPUsername, cfg.SIPPassword = "", ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation failure")
	}
	for _, want := range []string{"reolink_username", "reolink_password", "sip_username", "sip_password"} {
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

func TestLoadNormalizesIncomingCallerWhitelist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "options.json")
	data := `{"incoming_calls_enabled":true,"incoming_allowed_callers":[" 0123 456789 ","0123 456789"," **620 "],"dry_run":true}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0123 456789", "**620"}
	if len(cfg.IncomingAllowedCallers) != len(want) {
		t.Fatalf("normalized callers=%#v want=%#v", cfg.IncomingAllowedCallers, want)
	}
	for i := range want {
		if cfg.IncomingAllowedCallers[i] != want[i] {
			t.Fatalf("normalized callers=%#v want=%#v", cfg.IncomingAllowedCallers, want)
		}
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
