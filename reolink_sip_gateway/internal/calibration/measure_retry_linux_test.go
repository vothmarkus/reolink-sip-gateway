//go:build linux

package calibration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func TestOpenLatencyCaptureRetriesStartupAndKeepsFinalPCMSource(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "attempt")
	script := filepath.Join(dir, "fake-ffmpeg")
	body := fmt.Sprintf(`#!/bin/sh
set -eu
counter=%q
n=0
if [ -f "$counter" ]; then n=$(cat "$counter"); fi
n=$((n+1))
printf '%%s\n' "$n" > "$counter"
if [ "$n" -lt 3 ]; then
  echo "synthetic startup failure $n" >&2
  exit 1
fi
# 640 bytes = 320 mono s16 samples = 20 ms at 16 kHz. Keep the
# successful source paced in real time long enough for warmup + pacing checks.
i=0
while [ "$i" -lt 90 ]; do
  dd if=/dev/zero bs=640 count=1 2>/dev/null
  sleep 0.02
  i=$((i+1))
done
`, counter)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.FFmpegBinaryPath = script
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	capture, attempt, err := openLatencyCaptureWithRetry(ctx, cfg, "rtsp://example.invalid/stream", nil)
	if err != nil {
		t.Fatalf("retry capture failed: %v", err)
	}
	defer capture.close()
	if attempt != 3 {
		t.Fatalf("attempt=%d want 3", attempt)
	}
	if got := capture.collector.len(); got < latencyWarmupSamples {
		t.Fatalf("final capture has %d samples, want at least %d", got, latencyWarmupSamples)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "3\n" {
		t.Fatalf("counter=%q want 3", data)
	}
}
