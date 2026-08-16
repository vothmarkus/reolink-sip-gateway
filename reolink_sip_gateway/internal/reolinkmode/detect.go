package reolinkmode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtsp"
)

func Resolve(parent context.Context, cfg config.Config, logger *slog.Logger) (string, error) {
	return resolveWith(parent, cfg, logger, probeStandalone, probeNVR)
}

const autoProbeTimeout = 15 * time.Second

type standaloneProbe func(context.Context, config.Config, *slog.Logger) error
type nvrProbe func(context.Context, config.Config) error

func resolveWith(parent context.Context, cfg config.Config, logger *slog.Logger, standalone standaloneProbe, nvr nvrProbe) (string, error) {
	if cfg.ReolinkMode == "standalone" || cfg.ReolinkMode == "nvr" {
		return cfg.ReolinkMode, nil
	}
	if cfg.ReolinkMode != "auto" {
		return "", fmt.Errorf("unsupported Reolink mode %q", cfg.ReolinkMode)
	}

	standaloneCtx, cancelStandalone := context.WithTimeout(parent, autoProbeTimeout)
	standaloneErr := standalone(standaloneCtx, cfg, logger)
	cancelStandalone()
	if standaloneErr == nil {
		if logger != nil {
			logger.Info("automatic Reolink mode detection selected standalone RTSP/RTSP")
		}
		return "standalone", nil
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	if logger != nil {
		logger.Debug("standalone RTSP backchannel not available during auto detection", "error", standaloneErr)
	}
	// Give the NVR probe its own budget. A stalled standalone RTSP endpoint must
	// not consume the entire auto-detection window and prevent Baichuan fallback.
	nvrCtx, cancelNVR := context.WithTimeout(parent, autoProbeTimeout)
	nvrErr := nvr(nvrCtx, cfg)
	cancelNVR()
	if nvrErr == nil {
		if logger != nil {
			logger.Info("automatic Reolink mode detection selected NVR Baichuan/Baichuan")
		}
		return "nvr", nil
	}
	if err := parent.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("automatic Reolink mode detection failed: %w", errors.Join(
		fmt.Errorf("standalone RTSP backchannel: %w", standaloneErr),
		fmt.Errorf("NVR Baichuan: %w", nvrErr),
	))
}

func probeStandalone(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	client := rtsp.New(cfg.RTSPURL(), cfg.ReolinkUsername, cfg.ReolinkPassword, logger, cfg.DebugEnabled())
	if _, err := client.Open(ctx); err != nil {
		return err
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Shutdown(closeCtx); err != nil && logger != nil {
		logger.Debug("standalone capability probe cleanup failed after successful negotiation", "error", err)
	}
	return nil
}

func probeNVR(ctx context.Context, cfg config.Config) error {
	client, err := baichuan.Dial(ctx, baichuan.Config{
		Host: cfg.ReolinkHost, Port: cfg.BaichuanPort,
		Username: cfg.ReolinkUsername, Password: cfg.ReolinkPassword,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return err
	}
	defer client.Close()
	if _, err := client.PreferredTalkAudioProfile(ctx, uint8(cfg.NVRChannel)); err != nil {
		return err
	}
	return nil
}
