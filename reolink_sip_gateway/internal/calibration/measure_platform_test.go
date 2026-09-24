package calibration

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func TestBaichuanMeasurementConnectsWithoutFFmpeg(t *testing.T) {
	for _, mode := range []string{"direct", "nvr"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			connected := make(chan bool, 1)
			go func() {
				conn, err := listener.Accept()
				connected <- err == nil
				if err == nil {
					cancel()
					conn.Close()
				}
			}()
			cfg := config.Defaults().WithResolvedReolinkMode(mode)
			cfg.ReolinkHost = "127.0.0.1"
			cfg.BaichuanPort = listener.Addr().(*net.TCPAddr).Port
			cfg.FFmpegBinaryPath = filepath.Join(t.TempDir(), "no-ffmpeg-on-android")
			_, err = MeasureAcousticLatency(ctx, cfg, nil)
			if err == nil || strings.Contains(err.Error(), "ffmpeg not found") {
				t.Fatalf("measurement bypassed camera: %v", err)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if !<-connected {
				t.Fatal("measurement never connected to camera")
			}
		})
	}
}

func TestRTSPMeasurementStillRequiresFFmpeg(t *testing.T) {
	cfg := config.Defaults().WithResolvedReolinkMode("standalone")
	cfg.FFmpegBinaryPath = filepath.Join(t.TempDir(), "no-ffmpeg")
	_, err := MeasureAcousticLatency(context.Background(), cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg not found") {
		t.Fatalf("RTSP missing decoder: %v", err)
	}
}
