package standalone

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func validSettings() Settings {
	s := Defaults()
	s.Reolink.Host = "192.0.2.50"
	s.Reolink.Password = "camera-secret"
	s.SIP.Registrar = "192.0.2.1"
	return s
}

func TestPublicChannelsAndDefaultsConvertToSharedRuntime(t *testing.T) {
	for _, mode := range []string{"auto", "nvr", "standalone"} {
		s := validSettings()
		s.Reolink.Mode = mode
		s.Reolink.Channel = 7
		cfg, err := s.Runtime(false)
		if err != nil {
			t.Fatal(err)
		}
		channel, path := 6, "/Preview_07_sub"
		if mode == "standalone" {
			channel, path = 0, "/Preview_01_sub"
		}
		if cfg.NVRChannel != channel || cfg.ReolinkStreamPath != path || cfg.TriggerSource != "baichuan" || cfg.TriggerRouteID != "default" || cfg.CallRoutes[0].VisitorEntity != "" {
			t.Fatalf("mode %s has wrong runtime mapping", mode)
		}
		if !cfg.EchoCancellationEnabled || cfg.RTPInactivityTimeoutSeconds != 15 || !cfg.DryRun {
			t.Fatal("runtime defaults changed")
		}
	}
}

func TestSettingsRejectUnknownSchemaInvalidRouteAndMissingDirectCredentials(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `[]`, `{"schema_version":null}`} {
		if _, err := DecodeSettings([]byte(raw)); err == nil {
			t.Fatalf("unversioned configuration accepted: %s", raw)
		}
	}
	s := validSettings()
	raw, _ := json.Marshal(s)
	if _, err := DecodeSettings(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := DecodeSettings([]byte(`{"schema_version":3}`)); err == nil {
		t.Fatal("future schema accepted")
	}
	if _, err := DecodeSettings([]byte(`{"schema_version":2,"unsafe_key":true}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	s.Trigger.RouteID = "missing"
	if _, err := s.Runtime(false); err == nil {
		t.Fatal("unknown route accepted")
	}
	s = validSettings()
	s.Reolink.Password = ""
	if _, err := s.Runtime(false); err == nil {
		t.Fatal("direct trigger without credentials accepted")
	}
	s = validSettings()
	s.Reolink.Channel = 0
	if _, err := s.Runtime(false); err == nil {
		t.Fatal("zero public channel accepted")
	}
}

func TestAutomaticRegistrarUsesLowestMetricUsableDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route")
	routes := "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\neth0 00000000 0100000A 0003 0 0 500 00000000 0 0 0\nwlan0 00000000 01B2A8C0 0003 0 0 100 00000000 0 0 0\nbad0 00000000 0100000B 0000 0 0 1 00000000 0 0 0\n"
	if err := os.WriteFile(path, []byte(routes), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := DefaultIPv4Gateway(path)
	if err != nil || got != "192.168.178.1" {
		t.Fatalf("gateway=%s err=%v", got, err)
	}
	if _, err := DefaultIPv4Gateway(path + "missing"); err == nil {
		t.Fatal("missing routing table accepted")
	}
}
