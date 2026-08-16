package startup

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/calibration"
	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func testDeps(path string) dependencies {
	return dependencies{
		calibrationPath: path,
		now:             func() time.Time { return time.Date(2026, 8, 14, 22, 30, 0, 0, time.UTC) },
		resolveMode:     func(context.Context, config.Config, *slog.Logger) (string, error) { return "nvr", nil },
		measureLatency: func(context.Context, config.Config, *slog.Logger) (calibration.LatencyResult, error) {
			return calibration.LatencyResult{Delay: 1429 * time.Millisecond, Correlation: .55, SecondPeak: .15, PeakRatio: 3.66}, nil
		},
	}
}

func TestPrepareUsesMeasuredLatencyAndPersistsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.json")
	cfg := config.Defaults()
	cfg.DryRun = false
	deps := testDeps(path)
	got, err := prepareWith(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveMode != "nvr" || got.MediaProfile != "Baichuan ↔ Baichuan (sub)" || got.CalibrationStatus != "measured" {
		t.Fatalf("unexpected result: %#v", got)
	}
	if got.Config.AECInitialDelayMS != 1429 || got.Config.AECMinDelayMS != 1129 || got.Config.AECMaxDelayMS != 1729 {
		t.Fatalf("unexpected AEC range: %d %d..%d", got.Config.AECInitialDelayMS, got.Config.AECMinDelayMS, got.Config.AECMaxDelayMS)
	}
	rec, err := calibration.Load(path, got.Config)
	if err != nil || rec.DelayMS != 1429 {
		t.Fatalf("persisted calibration = %#v, %v", rec, err)
	}
}

func TestPrepareUsesMatchingCachedCalibrationOnMeasurementFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibration.json")
	cfg := config.Defaults()
	cfg.DryRun = false
	resolved := cfg.WithResolvedReolinkMode("nvr")
	when := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	if err := calibration.Save(path, resolved, 1437, when); err != nil {
		t.Fatal(err)
	}
	deps := testDeps(path)
	deps.measureLatency = func(context.Context, config.Config, *slog.Logger) (calibration.LatencyResult, error) {
		return calibration.LatencyResult{}, errors.New("ambiguous marker")
	}
	got, err := prepareWith(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.CalibrationStatus != "cached fallback" || got.Config.AECInitialDelayMS != 1437 || !got.LastCalibration.Equal(when) {
		t.Fatalf("unexpected fallback: %#v", got)
	}
}

func TestPrepareFallsBackToBuiltInWhenNoMatchingCacheExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	cfg := config.Defaults()
	cfg.DryRun = false
	deps := testDeps(path)
	deps.measureLatency = func(context.Context, config.Config, *slog.Logger) (calibration.LatencyResult, error) {
		return calibration.LatencyResult{}, errors.New("no marker")
	}
	got, err := prepareWith(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.CalibrationStatus != "safe fallback" || got.Config.AECInitialDelayMS != config.DefaultAECInitialDelayMS || !strings.Contains(got.CalibrationDetails, "no marker") {
		t.Fatalf("unexpected safe fallback: %#v", got)
	}
}

func TestPrepareRejectsOutOfRangeMeasurement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	cfg := config.Defaults()
	cfg.DryRun = false
	deps := testDeps(path)
	deps.measureLatency = func(context.Context, config.Config, *slog.Logger) (calibration.LatencyResult, error) {
		return calibration.LatencyResult{Delay: 3500 * time.Millisecond, Correlation: .8, PeakRatio: 4}, nil
	}
	got, err := prepareWith(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.CalibrationStatus != "safe fallback" || got.Config.AECInitialDelayMS != config.DefaultAECInitialDelayMS {
		t.Fatalf("unexpected out-of-range fallback: %#v", got)
	}
	if !strings.Contains(got.CalibrationDetails, "outside supported range") {
		t.Fatalf("missing range diagnostic: %s", got.CalibrationDetails)
	}
}

func TestPreparePropagatesParentCancellationInsteadOfFallingBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	cfg := config.Defaults()
	cfg.DryRun = false
	ctx, cancel := context.WithCancel(context.Background())
	deps := testDeps(path)
	deps.measureLatency = func(context.Context, config.Config, *slog.Logger) (calibration.LatencyResult, error) {
		cancel()
		return calibration.LatencyResult{}, context.Canceled
	}
	_, err := prepareWith(ctx, cfg, nil, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestDryRunNeverResolvesOrMeasures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	cfg := config.Defaults()
	cfg.DryRun = true
	cfg.ReolinkMode = "nvr"
	deps := testDeps(path)
	deps.resolveMode = func(context.Context, config.Config, *slog.Logger) (string, error) {
		t.Fatal("resolver called in dry-run")
		return "", nil
	}
	deps.measureLatency = func(context.Context, config.Config, *slog.Logger) (calibration.LatencyResult, error) {
		t.Fatal("measurement called in dry-run")
		return calibration.LatencyResult{}, nil
	}
	got, err := prepareWith(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveMode != "nvr" || got.CalibrationStatus != "skipped (dry run)" || got.Config.AECInitialDelayMS != config.DefaultAECInitialDelayMS {
		t.Fatalf("unexpected dry-run result: %#v", got)
	}
}

func TestAECDisabledSkipsMeasurement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.json")
	cfg := config.Defaults()
	cfg.DryRun = false
	cfg.EchoCancellationEnabled = false
	deps := testDeps(path)
	deps.measureLatency = func(context.Context, config.Config, *slog.Logger) (calibration.LatencyResult, error) {
		t.Fatal("measurement called with AEC disabled")
		return calibration.LatencyResult{}, nil
	}
	got, err := prepareWith(context.Background(), cfg, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveMode != "nvr" || got.CalibrationStatus != "AEC disabled" {
		t.Fatalf("unexpected result: %#v", got)
	}
}
