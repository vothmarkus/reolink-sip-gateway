package calibration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func TestCalibrationStoreRoundTripAndFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aec-calibration.json")
	cfg := config.Defaults().WithResolvedReolinkMode("nvr")
	when := time.Date(2026, 8, 14, 22, 0, 0, 0, time.UTC)
	if err := Save(path, cfg, 1429, when); err != nil {
		t.Fatal(err)
	}
	rec, err := Load(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rec.DelayMS != 1429 || rec.Mode != "nvr" || !rec.MeasuredAt.Equal(when) || rec.Fingerprint != Fingerprint(cfg) {
		t.Fatalf("unexpected record: %#v", rec)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("calibration permissions too broad: %o", info.Mode().Perm())
	}

	changed := cfg
	changed.NVRChannel++
	if _, err := Load(path, changed); err == nil {
		t.Fatal("changed NVR channel must invalidate calibration")
	}
	changed = cfg.WithResolvedReolinkMode("standalone")
	if _, err := Load(path, changed); err == nil {
		t.Fatal("changed media profile must invalidate calibration")
	}
}

func TestCalibrationStoreRejectsInvalidDelay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aec-calibration.json")
	cfg := config.Defaults().WithResolvedReolinkMode("nvr")
	data := `{"version":1,"fingerprint":"` + Fingerprint(cfg) + `","mode":"nvr","delay_ms":4001,"measured_at":"2026-08-14T20:00:00Z"}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, cfg); err == nil {
		t.Fatal("invalid stored delay accepted")
	}
}

func TestCalibrationStoreRejectsInvalidSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aec-calibration.json")
	cfg := config.Defaults().WithResolvedReolinkMode("nvr")
	if err := Save(path, cfg, config.MaxSupportedAECDelayMS+1, time.Now()); err == nil {
		t.Fatal("out-of-range calibration was persisted")
	}
	unresolved := config.Defaults()
	if err := Save(path, unresolved, 1429, time.Now()); err == nil {
		t.Fatal("unresolved calibration profile was persisted")
	}
}
