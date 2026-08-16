package startup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/calibration"
	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/reolinkmode"
)

type Result struct {
	Config             config.Config
	ActiveMode         string
	MediaProfile       string
	CalibrationStatus  string
	CalibrationDetails string
	LastCalibration    time.Time
}

type dependencies struct {
	resolveMode     func(context.Context, config.Config, *slog.Logger) (string, error)
	measureLatency  func(context.Context, config.Config, *slog.Logger) (calibration.LatencyResult, error)
	calibrationPath string
	now             func() time.Time
}

var defaultDependencies = dependencies{
	resolveMode:     reolinkmode.Resolve,
	measureLatency:  calibration.MeasureAcousticLatency,
	calibrationPath: calibration.FilePath,
	now:             time.Now,
}

func Prepare(ctx context.Context, cfg config.Config, logger *slog.Logger) (Result, error) {
	return prepareWith(ctx, cfg, logger, defaultDependencies)
}

func prepareWith(ctx context.Context, cfg config.Config, logger *slog.Logger, deps dependencies) (Result, error) {
	result := Result{Config: cfg, CalibrationStatus: "not required"}
	if deps.resolveMode == nil || deps.measureLatency == nil || deps.now == nil || deps.calibrationPath == "" {
		return result, fmt.Errorf("invalid startup dependencies")
	}

	if cfg.DryRun {
		// Dry-run is intentionally side-effect free: no capability negotiation and,
		// most importantly, no audible calibration marker.
		if cfg.ReolinkMode == "standalone" || cfg.ReolinkMode == "nvr" {
			cfg = cfg.WithResolvedReolinkMode(cfg.ReolinkMode)
			result.ActiveMode = cfg.EffectiveReolinkMode()
			result.MediaProfile = mediaProfile(result.ActiveMode)
		}
		result.Config = cfg
		if cfg.EchoCancellationEnabled {
			if rec, err := calibration.Load(deps.calibrationPath, cfg); err == nil {
				cfg.SetAECDelay(rec.DelayMS)
				result.Config = cfg
				result.CalibrationStatus = "cached (dry run)"
				result.CalibrationDetails = fmt.Sprintf("%d ms from persistent calibration", rec.DelayMS)
				result.LastCalibration = rec.MeasuredAt
			} else {
				cfg.SetAECDelay(config.DefaultAECInitialDelayMS)
				result.Config = cfg
				result.CalibrationStatus = "skipped (dry run)"
				result.CalibrationDetails = fmt.Sprintf("fallback %d ms; no acoustic marker emitted", cfg.AECInitialDelayMS)
			}
		}
		return result, nil
	}

	mode, err := deps.resolveMode(ctx, cfg, logger)
	if err != nil {
		return result, err
	}
	cfg = cfg.WithResolvedReolinkMode(mode)
	result.ActiveMode = mode
	result.MediaProfile = mediaProfile(mode)
	result.Config = cfg

	if !cfg.EchoCancellationEnabled {
		result.CalibrationStatus = "AEC disabled"
		return result, nil
	}

	if logger != nil {
		logger.Info("automatic acoustic latency calibration starting",
			"mode", mode,
			"receive_mode", cfg.ReceiveMode(),
			"search_window_ms", cfg.EchoCancellationSearchWindowMS)
	}
	measured, measureErr := deps.measureLatency(ctx, cfg, logger)
	if measureErr != nil && ctx.Err() != nil {
		return result, ctx.Err()
	}
	delayMS := int(measured.Delay.Round(time.Millisecond) / time.Millisecond)
	if measureErr == nil && (delayMS < 0 || delayMS > config.MaxSupportedAECDelayMS) {
		measureErr = fmt.Errorf("measured acoustic latency %d ms is outside supported range 0..%d ms", delayMS, config.MaxSupportedAECDelayMS)
	}
	if measureErr == nil {
		cfg.SetAECDelay(delayMS)
		now := deps.now()
		result.Config = cfg
		result.CalibrationStatus = "measured"
		result.CalibrationDetails = fmt.Sprintf("%d ms, correlation %.3f, peak ratio %.2f", delayMS, measured.Correlation, measured.PeakRatio)
		result.LastCalibration = now
		if logger != nil {
			logger.Info("automatic acoustic latency calibration accepted",
				"delay_ms", delayMS,
				"search_min_ms", cfg.AECMinDelayMS,
				"search_max_ms", cfg.AECMaxDelayMS,
				"correlation", measured.Correlation,
				"second_peak", measured.SecondPeak,
				"peak_ratio", measured.PeakRatio)
		}
		if err := calibration.Save(deps.calibrationPath, cfg, delayMS, now); err != nil && logger != nil {
			logger.Warn("could not persist AEC calibration; current measurement remains active", "error", err)
		}
		return result, nil
	}

	if rec, err := calibration.Load(deps.calibrationPath, cfg); err == nil {
		cfg.SetAECDelay(rec.DelayMS)
		result.Config = cfg
		result.CalibrationStatus = "cached fallback"
		result.CalibrationDetails = fmt.Sprintf("measurement failed (%v); using %d ms from %s", measureErr, rec.DelayMS, rec.MeasuredAt.Format(time.RFC3339))
		result.LastCalibration = rec.MeasuredAt
		if logger != nil {
			logger.Warn("automatic acoustic latency calibration failed; using persisted calibration",
				"error", measureErr, "delay_ms", rec.DelayMS, "measured_at", rec.MeasuredAt)
		}
		return result, nil
	}

	cfg.SetAECDelay(config.DefaultAECInitialDelayMS)
	result.Config = cfg
	result.CalibrationStatus = "safe fallback"
	result.CalibrationDetails = fmt.Sprintf("measurement failed (%v); using built-in %d ms", measureErr, cfg.AECInitialDelayMS)
	if logger != nil {
		logger.Warn("automatic acoustic latency calibration failed; using built-in fallback",
			"error", measureErr,
			"delay_ms", cfg.AECInitialDelayMS,
			"search_min_ms", cfg.AECMinDelayMS,
			"search_max_ms", cfg.AECMaxDelayMS)
	}
	return result, nil
}

func mediaProfile(mode string) string {
	switch mode {
	case "standalone":
		return "RTSP ↔ RTSP"
	case "nvr":
		return "Baichuan ↔ Baichuan (sub)"
	default:
		return ""
	}
}
