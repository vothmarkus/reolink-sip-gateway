package mobilebridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/standalone"
)

func TestImportNormalizesStandaloneDefaultsAndKeepsRoutesAndSecrets(t *testing.T) {
	s := androidSettings()
	s.Reolink.Password = " camera secret \" \\ \n "
	s.Reolink.Mode = " Direct "
	s.SIP.Codec = " PCMU "
	s.Diagnostics.LogLevel = "warning"
	s.CallRoutes = append(s.CallRoutes, config.CallRoute{ID: "garden", Name: "Garten", DoorbellNumber: "12", MobileNumber1: "**610"})
	s.Trigger = standalone.TriggerSettings{Source: "manual", RouteID: "garden"}
	raw, err := NormalizeConfig(encodeSettings(t, s))
	if err != nil {
		t.Fatal(err)
	}
	var imported standalone.Settings
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Reolink.Password != s.Reolink.Password || imported.Reolink.Mode != "direct" || imported.SIP.Codec != "pcmu" || imported.Diagnostics.LogLevel != "warn" {
		t.Fatal("normalization changed credentials or failed to normalize selection values")
	}
	if len(imported.CallRoutes) != 2 || imported.CallRoutes[1].MobileNumber1 != "**610" || imported.Trigger != s.Trigger {
		t.Fatal("import lost routing")
	}
	if err := ValidateConfig(raw); err != nil {
		t.Fatal("normalized document cannot be used:", err)
	}
	// Missing groups must use the standalone contract, not Android UI defaults.
	var partial map[string]any
	if err := json.Unmarshal([]byte(raw), &partial); err != nil {
		t.Fatal(err)
	}
	delete(partial, "audio")
	delete(partial, "call")
	b, _ := json.Marshal(partial)
	raw, err = NormalizeConfig(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Call.RingTimeout != 30 || !imported.Audio.AEC {
		t.Fatal("standalone defaults were not expanded")
	}
	partial["call_routes"] = nil
	b, _ = json.Marshal(partial)
	raw, err = NormalizeConfig(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.CallRoutes) != 1 || imported.CallRoutes[0].ID != "default" {
		t.Fatal("legacy/default runtime route was not restored for the editor")
	}
}

func TestImportRejectsUnsupportedOrAmbiguousDocuments(t *testing.T) {
	valid := encodeSettings(t, androidSettings())
	for name, raw := range map[string]string{
		"unknown field": strings.Replace(valid, `"schema_version":2`, `"schema_version":2,"unexpected":true`, 1),
		"new schema":    strings.Replace(valid, `"schema_version":2`, `"schema_version":3`, 1),
		"trailing JSON": valid + `{}`,
		"RTSP mode":     strings.Replace(valid, `"reolink_mode":"direct"`, `"reolink_mode":"standalone"`, 1),
		"auto SIP":      strings.Replace(valid, `"sip_registrar":"192.0.2.1"`, `"sip_registrar":"auto"`, 1),
		"missing route": strings.Replace(valid, `"route_id":"default"`, `"route_id":"missing"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := NormalizeConfig(raw); err == nil || got != "" {
				t.Fatal("invalid import returned a configuration")
			}
		})
	}
}
