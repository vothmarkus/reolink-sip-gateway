package calibration

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func TestCaptureTermination(t *testing.T) {
	live := make(chan error, 1)
	if err := captureTermination(live); err != nil {
		t.Fatalf("live capture reported ended: %v", err)
	}

	failed := make(chan error, 1)
	failed <- errors.New("boom")
	close(failed)
	if err := captureTermination(failed); err == nil {
		t.Fatal("failed capture was not detected")
	}

	ended := make(chan error)
	close(ended)
	if err := captureTermination(ended); err == nil {
		t.Fatal("cleanly ended capture was not detected")
	}
}

func TestEstimateAcousticDelaySynthetic(t *testing.T) {
	rate := latencyCaptureRate
	ref := generateLatencyMarker(rate)
	delaySamples := int(1375 * time.Millisecond.Seconds() * float64(rate))
	capture := make([]int16, delaySamples+len(ref)+2*rate)
	// Add deterministic low-level ambient content and an attenuated copy of the
	// marker. The coded speech-band marker should remain both detectable and
	// unique despite the unrelated tone.
	for i := range capture {
		capture[i] = int16(220*math.Sin(2*math.Pi*73*float64(i)/float64(rate)) + 130*math.Sin(2*math.Pi*311*float64(i)/float64(rate)))
	}
	for i, v := range ref {
		mixed := int(capture[delaySamples+i]) + int(v)/4
		capture[delaySamples+i] = clampInt16(mixed)
	}
	peaks, ok := estimateAcousticDelay(capture, ref, rate)
	if !ok {
		t.Fatal("correlation failed")
	}
	if d := peaks.Delay - 1375*time.Millisecond; d < -3*time.Millisecond || d > 3*time.Millisecond {
		t.Fatalf("delay=%s want about 1.375s (best=%.3f second=%.3f)", peaks.Delay, peaks.Best, peaks.Second)
	}
	if peaks.Best < 0.65 {
		t.Fatalf("correlation score too low: %.3f", peaks.Best)
	}
	if !peaks.Reliable {
		t.Fatalf("expected unique reliable peak, got best=%.3f second=%.3f margin=%.3f ratio=%.2f", peaks.Best, peaks.Second, peaks.Margin, peaks.Ratio)
	}
}

func TestEstimateAcousticDelaySurvivesSimpleSpeakerMicFiltering(t *testing.T) {
	rate := latencyCaptureRate
	ref := generateLatencyMarker(rate)
	delay := 870 * time.Millisecond
	delaySamples := int(delay.Seconds() * float64(rate))
	capture := make([]int16, 5*rate)
	for i := range capture {
		capture[i] = int16(360*math.Sin(2*math.Pi*127*float64(i)/float64(rate)) + 260*math.Sin(2*math.Pi*487*float64(i)/float64(rate)))
	}
	// Crude causal low-pass plus soft attenuation. This is not a physical
	// Doorbell model, but it verifies that the matched marker does not require
	// byte-identical PCM or a flat acoustic frequency response.
	filtered := make([]int16, len(ref))
	var y float64
	for i, v := range ref {
		y += 0.42 * (float64(v) - y)
		filtered[i] = int16(y * 0.28)
	}
	for i, v := range filtered {
		capture[delaySamples+i] = clampInt16(int(capture[delaySamples+i]) + int(v))
	}
	peaks, ok := estimateAcousticDelay(capture, ref, rate)
	if !ok || !peaks.Reliable {
		t.Fatalf("filtered marker not detected reliably: ok=%t best=%.3f second=%.3f margin=%.3f ratio=%.2f", ok, peaks.Best, peaks.Second, peaks.Margin, peaks.Ratio)
	}
	if d := peaks.Delay - delay; d < -5*time.Millisecond || d > 5*time.Millisecond {
		t.Fatalf("delay=%s want about %s", peaks.Delay, delay)
	}
}

func TestEstimateAcousticDelayRejectsTwoIndependentMarkerPeaks(t *testing.T) {
	rate := latencyCaptureRate
	ref := generateLatencyMarker(rate)
	capture := make([]int16, 4*rate)
	first := int(700 * time.Millisecond.Seconds() * float64(rate))
	second := int(2300 * time.Millisecond.Seconds() * float64(rate))
	for i, v := range ref {
		capture[first+i] = v / 3
		capture[second+i] = v / 3
	}
	peaks, ok := estimateAcousticDelay(capture, ref, rate)
	if !ok {
		t.Fatal("correlation failed")
	}
	if peaks.Reliable {
		t.Fatalf("two equally strong independent markers must be ambiguous: best=%.3f second=%.3f margin=%.3f ratio=%.2f", peaks.Best, peaks.Second, peaks.Margin, peaks.Ratio)
	}
	if peaks.Second < 0.8*peaks.Best {
		t.Fatalf("second independent peak unexpectedly weak: best=%.3f second=%.3f", peaks.Best, peaks.Second)
	}
}

func TestEstimateAcousticDelayRejectsAmbientOnly(t *testing.T) {
	rate := latencyCaptureRate
	ref := generateLatencyMarker(rate)
	capture := make([]int16, 5*rate)
	for i := range capture {
		// A deterministic mixture of unrelated tones approximates structured
		// ambient content without making the test depend on a random seed.
		v := 520*math.Sin(2*math.Pi*317*float64(i)/float64(rate)) +
			340*math.Sin(2*math.Pi*911*float64(i)/float64(rate)) +
			230*math.Sin(2*math.Pi*2143*float64(i)/float64(rate))
		capture[i] = int16(v)
	}
	peaks, ok := estimateAcousticDelay(capture, ref, rate)
	if !ok {
		t.Fatal("correlation evaluation failed")
	}
	if peaks.Reliable {
		t.Fatalf("ambient-only capture must not be accepted: best=%.3f second=%.3f margin=%.3f ratio=%.2f", peaks.Best, peaks.Second, peaks.Margin, peaks.Ratio)
	}
}

func TestLatencyMarkerHasExpectedDurationAndLevel(t *testing.T) {
	if len(latencyMarkerCode) != latencyMarkerSymbols {
		t.Fatalf("marker code symbols=%d want=%d", len(latencyMarkerCode), latencyMarkerSymbols)
	}
	for _, rate := range []int{8000, 16000, 44100, 48000} {
		marker := generateLatencyMarker(rate)
		want := int(math.Round(latencyMarkerDuration.Seconds() * float64(rate)))
		if len(marker) != want {
			t.Fatalf("rate=%d marker samples=%d want=%d", rate, len(marker), want)
		}
		var energy float64
		var peak int16
		for _, v := range marker {
			fv := float64(v)
			energy += fv * fv
			if abs16(v) > abs16(peak) {
				peak = v
			}
		}
		rms := math.Sqrt(energy / float64(len(marker)))
		if rms < 3500 || rms > 8000 {
			t.Fatalf("rate=%d unexpected marker RMS %.1f", rate, rms)
		}
		if abs16(peak) < 8500 || abs16(peak) > int16(latencyMarkerAmplitude)+2 {
			t.Fatalf("rate=%d unexpected marker peak %d", rate, peak)
		}
	}
}

func TestLatencyCaptureArgsUseStableTCPBaseline(t *testing.T) {
	cfg := config.Defaults()
	args := buildLatencyCaptureArgs(cfg, "rtsp://example.invalid/stream")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-rtsp_transport tcp", "-fflags nobuffer", "-flags low_delay", "-ar 16000"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	for _, retired := range []string{"-reorder_queue_size", "-max_delay", "-flush_packets", "-probesize 32768"} {
		if strings.Contains(joined, retired) {
			t.Fatalf("retired tuning flag %q still present in %v", retired, args)
		}
	}
}

func clampInt16(v int) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

func abs16(v int16) int16 {
	if v < 0 {
		if v == -32768 {
			return 32767
		}
		return -v
	}
	return v
}

func containsArgPairOrText(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestLatencyRedactRemovesCredentials(t *testing.T) {
	cfg := config.Defaults()
	cfg.ReolinkPassword = "p@ss word"
	got := redact("failed rtsp://user:p%40ss%20word@192.0.2.1/live; plain=p@ss word", cfg)
	if strings.Contains(got, "user:") || strings.Contains(got, "p%40ss") || strings.Contains(got, "p@ss") {
		t.Fatalf("credentials leaked: %q", got)
	}
}
