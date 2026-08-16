package reolinkmode

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func TestExplicitModeDoesNotProbe(t *testing.T) {
	for _, mode := range []string{"standalone", "nvr"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.ReolinkMode = mode
			standaloneCalls, nvrCalls := 0, 0
			got, err := resolveWith(context.Background(), cfg, nil,
				func(context.Context, config.Config, *slog.Logger) error { standaloneCalls++; return nil },
				func(context.Context, config.Config) error { nvrCalls++; return nil })
			if err != nil || got != mode {
				t.Fatalf("mode=%q err=%v, want %q", got, err, mode)
			}
			if standaloneCalls != 0 || nvrCalls != 0 {
				t.Fatalf("explicit mode unexpectedly probed standalone=%d nvr=%d", standaloneCalls, nvrCalls)
			}
		})
	}
}

func TestAutoSelectsOneCompleteProfile(t *testing.T) {
	cfg := config.Defaults()
	cfg.ReolinkMode = "auto"

	t.Run("standalone wins", func(t *testing.T) {
		standaloneCalls, nvrCalls := 0, 0
		got, err := resolveWith(context.Background(), cfg, nil,
			func(context.Context, config.Config, *slog.Logger) error { standaloneCalls++; return nil },
			func(context.Context, config.Config) error { nvrCalls++; return nil })
		if err != nil || got != "standalone" || standaloneCalls != 1 || nvrCalls != 0 {
			t.Fatalf("got=%q err=%v calls=%d/%d", got, err, standaloneCalls, nvrCalls)
		}
	})

	t.Run("NVR after standalone failure", func(t *testing.T) {
		standaloneCalls, nvrCalls := 0, 0
		got, err := resolveWith(context.Background(), cfg, nil,
			func(context.Context, config.Config, *slog.Logger) error {
				standaloneCalls++
				return errors.New("no backchannel")
			},
			func(context.Context, config.Config) error { nvrCalls++; return nil })
		if err != nil || got != "nvr" || standaloneCalls != 1 || nvrCalls != 1 {
			t.Fatalf("got=%q err=%v calls=%d/%d", got, err, standaloneCalls, nvrCalls)
		}
	})
}

func TestAutoReportsBothProbeFailures(t *testing.T) {
	cfg := config.Defaults()
	cfg.ReolinkMode = "auto"
	_, err := resolveWith(context.Background(), cfg, nil,
		func(context.Context, config.Config, *slog.Logger) error { return errors.New("rtsp failed") },
		func(context.Context, config.Config) error { return errors.New("baichuan failed") })
	if err == nil || !strings.Contains(err.Error(), "rtsp failed") || !strings.Contains(err.Error(), "baichuan failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAutoProbesReceiveIndependentTimeoutBudgets(t *testing.T) {
	cfg := config.Defaults()
	cfg.ReolinkMode = "auto"
	got, err := resolveWith(context.Background(), cfg, nil,
		func(ctx context.Context, _ config.Config, _ *slog.Logger) error {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > autoProbeTimeout+time.Second {
				t.Fatalf("unexpected standalone deadline: %v ok=%v", deadline, ok)
			}
			return context.DeadlineExceeded
		},
		func(ctx context.Context, _ config.Config) error {
			if err := ctx.Err(); err != nil {
				t.Fatalf("NVR fallback received an already-cancelled context: %v", err)
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > autoProbeTimeout+time.Second {
				t.Fatalf("unexpected NVR deadline: %v ok=%v", deadline, ok)
			}
			return nil
		})
	if err != nil || got != "nvr" {
		t.Fatalf("got=%q err=%v, want nvr", got, err)
	}
}

func TestAutoStopsOnParentCancellationBeforeFallback(t *testing.T) {
	cfg := config.Defaults()
	cfg.ReolinkMode = "auto"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nvrCalls := 0
	_, err := resolveWith(ctx, cfg, nil,
		func(ctx context.Context, _ config.Config, _ *slog.Logger) error { return ctx.Err() },
		func(context.Context, config.Config) error { nvrCalls++; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
	if nvrCalls != 0 {
		t.Fatalf("NVR fallback called %d times after parent cancellation", nvrCalls)
	}
}
