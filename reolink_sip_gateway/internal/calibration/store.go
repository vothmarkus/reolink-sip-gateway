package calibration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

const FilePath = "/data/aec-calibration.json"

type Record struct {
	Version     int       `json:"version"`
	Fingerprint string    `json:"fingerprint"`
	Mode        string    `json:"mode"`
	DelayMS     int       `json:"delay_ms"`
	MeasuredAt  time.Time `json:"measured_at"`
}

func Fingerprint(cfg config.Config) string {
	return fmt.Sprintf("%s|%s|rtsp:%d|path:%s|bc:%d|ch:%d",
		cfg.ReolinkHost, cfg.EffectiveReolinkMode(), cfg.ReolinkRTSPPort,
		cfg.ReolinkStreamPath, cfg.BaichuanPort, cfg.NVRChannel)
}

func Load(path string, cfg config.Config) (Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		return Record{}, fmt.Errorf("decode calibration: %w", err)
	}
	if rec.Version != 1 || rec.Fingerprint != Fingerprint(cfg) || rec.DelayMS < 0 || rec.DelayMS > config.MaxSupportedAECDelayMS {
		return Record{}, errors.New("stored calibration does not match the active Reolink profile")
	}
	return rec, nil
}

func Save(path string, cfg config.Config, delayMS int, measuredAt time.Time) error {
	if delayMS < 0 || delayMS > config.MaxSupportedAECDelayMS {
		return fmt.Errorf("calibration delay must be 0..%d ms", config.MaxSupportedAECDelayMS)
	}
	mode := cfg.EffectiveReolinkMode()
	if mode != "standalone" && mode != "nvr" {
		return fmt.Errorf("calibration requires a resolved Reolink mode, got %q", mode)
	}
	rec := Record{Version: 1, Fingerprint: Fingerprint(cfg), Mode: mode, DelayMS: delayMS, MeasuredAt: measuredAt}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
