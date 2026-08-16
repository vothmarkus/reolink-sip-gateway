package calibration

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuanaudio"
	"github.com/vothmarkus/reolink-sip-gateway/internal/codec"
	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtsp"
)

const (
	latencyTestTimeout          = 60 * time.Second
	latencyCaptureRate          = 16000
	latencyWarmupSamples        = latencyCaptureRate / 2 // 500 ms of flowing RTSP audio before marker.
	latencySearchDuration       = 5 * time.Second
	latencyMarkerSymbolDuration = 64 * time.Millisecond
	latencyMarkerSymbols        = 16
	latencyMarkerDuration       = latencyMarkerSymbolDuration * latencyMarkerSymbols
	latencyMarkerAmplitude      = 10000.0
	latencyCorrelationFloor     = 0.12
	latencyPeakMarginFloor      = 0.035
	latencyPeakRatioFloor       = 1.20
	latencyIndependentPeakGuard = latencyMarkerDuration
	latencyCaptureAttempts      = 3
	latencyCaptureRetryDelay    = 750 * time.Millisecond
)

var latencyMarkerFrequencies = [...]float64{850, 1200, 1700, 2300}

// The order deliberately does not repeat a short periodic pattern. Each
// symbol selects one frequency from latencyMarkerFrequencies. Combined with
// per-symbol fades this produces a robust, speech-band marker that survives
// the Doorbell/NVR speaker, microphone and AAC path substantially better than
// the former low-frequency sweep.
var latencyMarkerCode = [...]uint8{0, 3, 1, 2, 3, 0, 2, 1, 2, 0, 3, 1, 1, 3, 0, 2}

var rtspUserInfoPattern = regexp.MustCompile(`(?i)rtsp://[^\s/@]+(?::[^\s@]*)?@`)

func redact(v string, cfg config.Config) string {
	v = rtspUserInfoPattern.ReplaceAllString(v, "rtsp://***@")
	if cfg.ReolinkPassword != "" {
		v = strings.ReplaceAll(v, cfg.ReolinkPassword, "***")
		v = strings.ReplaceAll(v, url.PathEscape(cfg.ReolinkPassword), "***")
		v = strings.ReplaceAll(v, url.QueryEscape(cfg.ReolinkPassword), "***")
	}
	return v
}

type LatencyResult struct {
	Delay                time.Duration
	Correlation          float64
	SecondPeak           float64
	PeakMargin           float64
	PeakRatio            float64
	ReceiveMode          string
	ReceiveDetails       string
	ReceiveCodec         string
	Channel              int
	SampleRate           int
	SamplesScanned       int
	CaptureAttempt       int
	MarkerBlocks         int
	MarkerBlocksExpected int
	MarkerDuration       time.Duration
	MarkerElapsed        time.Duration
}

// LatencyAmbiguousError means the capture and marker transmission succeeded,
// but the acoustic return did not contain one sufficiently unique correlation
// peak. It is intentionally distinct from an operational failure such as a
// broken RTSP capture or Baichuan session.
type LatencyAmbiguousError struct {
	Best   float64
	Second float64
	Margin float64
	Ratio  float64
}

func (e *LatencyAmbiguousError) Error() string {
	return fmt.Sprintf(
		"acoustic marker result ambiguous (best correlation %.3f, second independent peak %.3f, margin %.3f, ratio %.2f; require best >= %.2f and margin >= %.3f or ratio >= %.2f)",
		e.Best, e.Second, e.Margin, e.Ratio, latencyCorrelationFloor, latencyPeakMarginFloor, latencyPeakRatioFloor,
	)
}

type latencyCapture struct {
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	collector *pcmCollector
	done      chan error
	stderr    *bytes.Buffer
	waitOnce  sync.Once
}

func (c *latencyCapture) close() {
	if c == nil {
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.done != nil {
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
		}
	}
	if c.cmd != nil {
		c.waitOnce.Do(func() { _ = c.cmd.Wait() })
	}
}

func (c *latencyCapture) stderrText(cfg config.Config) string {
	if c == nil || c.stderr == nil {
		return ""
	}
	return redact(c.stderr.String(), cfg)
}

type markerSendStats struct {
	BlocksSent     int
	BlocksExpected int
	MediaDuration  time.Duration
	Elapsed        time.Duration
}

type pcmCollector struct {
	mu      sync.Mutex
	samples []int16
}

func (c *pcmCollector) append(samples []int16) {
	c.mu.Lock()
	c.samples = append(c.samples, samples...)
	c.mu.Unlock()
}

func (c *pcmCollector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.samples)
}

func (c *pcmCollector) slice(from, to int) []int16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if from < 0 {
		from = 0
	}
	if to > len(c.samples) {
		to = len(c.samples)
	}
	if from > to {
		from = to
	}
	return append([]int16(nil), c.samples[from:to]...)
}

// MeasureAcousticLatency measures the local Reolink acoustic loop. It uses the
// already resolved full transport profile: standalone sends the marker through
// the ONVIF RTSP backchannel and captures RTSP; NVR sends it through Baichuan
// and captures the fixed Baichuan sub stream. Correlation is performed inside
// the gateway, so SIP, PBX, VPN and handset latency are intentionally excluded.
func MeasureAcousticLatency(parent context.Context, cfg config.Config, logger *slog.Logger) (LatencyResult, error) {
	result := LatencyResult{
		ReceiveMode: cfg.ReceiveMode(),
		Channel:     cfg.NVRChannel,
		SampleRate:  latencyCaptureRate,
	}
	if _, err := exec.LookPath(cfg.FFmpegPath()); err != nil {
		return result, fmt.Errorf("ffmpeg not found at %q: %w", cfg.FFmpegPath(), err)
	}

	ctx, cancel := context.WithTimeout(parent, latencyTestTimeout)
	defer cancel()

	var (
		collector    *pcmCollector
		captureDone  <-chan error
		closeCapture func()
		attempt      int
	)
	switch cfg.ReceiveMode() {
	case "rtsp":
		u := &url.URL{
			Scheme: "rtsp",
			Host:   net.JoinHostPort(cfg.ReolinkHost, strconv.Itoa(cfg.ReolinkRTSPPort)),
			Path:   cfg.ReolinkStreamPath,
			User:   url.UserPassword(cfg.ReolinkUsername, cfg.ReolinkPassword),
		}
		capture, captureAttempt, err := openLatencyCaptureWithRetry(ctx, cfg, u.String(), logger)
		if err != nil {
			return result, err
		}
		attempt = captureAttempt
		collector = capture.collector
		captureDone = capture.done
		closeCapture = capture.close
		result.ReceiveDetails = "RTSP/TCP " + cfg.ReolinkStreamPath
	case "baichuan":
		capture, captureAttempt, info, err := openBaichuanLatencyCaptureWithRetry(ctx, cfg, logger)
		if err != nil {
			return result, err
		}
		attempt = captureAttempt
		collector = capture.collector
		captureDone = capture.done
		closeCapture = capture.close
		result.ReceiveCodec = info.Codec
		result.ReceiveDetails = info.Details()
	default:
		return result, fmt.Errorf("unsupported receive_mode %q", cfg.ReceiveMode())
	}
	result.CaptureAttempt = attempt
	defer closeCapture()

	// Open the talk path that belongs to the resolved profile. No mixed mode is
	// allowed here: the startup measurement must exercise the same physical path
	// that later supplies the AEC render reference.
	var (
		markerRate int
		sendMarker func([]int16) (markerSendStats, error)
		closeTalk  func()
		markerPath string
	)
	switch cfg.EffectiveReolinkMode() {
	case "nvr":
		client, err := baichuan.Dial(ctx, baichuan.Config{
			Host: cfg.ReolinkHost, Port: cfg.BaichuanPort,
			Username: cfg.ReolinkUsername, Password: cfg.ReolinkPassword,
			Timeout: 10 * time.Second,
		})
		if err != nil {
			return result, fmt.Errorf("connect Baichuan for latency calibration: %w", err)
		}
		session, err := client.StartTalk(ctx, uint8(cfg.NVRChannel))
		if err != nil {
			_ = client.Close()
			return result, fmt.Errorf("start Baichuan talk for latency calibration: %w", err)
		}
		if session.SampleRate() <= 0 || session.SamplesPerBlock() < 2 || session.SamplesPerBlock()%2 != 0 || !strings.EqualFold(session.AudioType(), "adpcm") {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = session.Close(closeCtx)
			closeCancel()
			_ = client.Close()
			return result, fmt.Errorf("unsupported Baichuan profile for latency calibration: codec=%s rate=%d samples_per_block=%d", session.AudioType(), session.SampleRate(), session.SamplesPerBlock())
		}
		markerRate = session.SampleRate()
		markerPath = "Baichuan"
		sendMarker = func(pcm []int16) (markerSendStats, error) { return sendPCMThroughBaichuan(ctx, client, session, pcm) }
		closeTalk = func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = session.Close(closeCtx)
			closeCancel()
			_ = client.Close()
		}
	case "standalone":
		client := rtsp.New(cfg.RTSPURL(), cfg.ReolinkUsername, cfg.ReolinkPassword, logger, cfg.DebugEnabled())
		bc, err := client.Open(ctx)
		if err != nil {
			return result, fmt.Errorf("open RTSP backchannel for latency calibration: %w", err)
		}
		markerRate = 8000
		markerPath = "ONVIF RTSP backchannel"
		sendMarker = func(pcm []int16) (markerSendStats, error) { return sendPCMThroughRTSP(ctx, client, bc, pcm) }
		closeTalk = func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = client.Shutdown(closeCtx)
			closeCancel()
		}
	default:
		return result, fmt.Errorf("latency calibration requires a resolved Reolink mode, got %q", cfg.EffectiveReolinkMode())
	}
	defer closeTalk()

	// Give the newly negotiated full-duplex path a short settling interval; this
	// interval is not part of the measured baseline.
	select {
	case <-ctx.Done():
		return result, ctx.Err()
	case <-time.After(150 * time.Millisecond):
	}
	if err := captureTermination(captureDone); err != nil {
		return result, fmt.Errorf("%s latency capture ended before acoustic marker: %w", cfg.ReceiveMode(), err)
	}

	markerTalk := generateLatencyMarker(markerRate)
	markerCapture := generateLatencyMarker(latencyCaptureRate)
	baseline := collector.len()
	if logger != nil {
		logger.Warn("automatic acoustic latency calibration marker starting; a coded speech-band marker will be audible at the doorbell",
			"mode", cfg.EffectiveReolinkMode(), "talk_path", markerPath, "channel", cfg.NVRChannel, "duration", latencyMarkerDuration,
			"frequency_min_hz", latencyMarkerFrequencies[0], "frequency_max_hz", latencyMarkerFrequencies[len(latencyMarkerFrequencies)-1],
			"receive_mode", cfg.ReceiveMode(), "receive_details", result.ReceiveDetails)
	}
	markerStats, err := sendMarker(markerTalk)
	if err != nil {
		return result, err
	}
	result.MarkerBlocks = markerStats.BlocksSent
	result.MarkerBlocksExpected = markerStats.BlocksExpected
	result.MarkerDuration = markerStats.MediaDuration
	result.MarkerElapsed = markerStats.Elapsed
	if logger != nil {
		logger.Info("automatic acoustic latency calibration marker transmitted",
			"mode", cfg.EffectiveReolinkMode(), "talk_path", markerPath,
			"blocks_sent", markerStats.BlocksSent, "blocks_total", markerStats.BlocksExpected,
			"media_duration", markerStats.MediaDuration, "send_elapsed", markerStats.Elapsed)
	}

	targetSamples := baseline + int(latencySearchDuration.Seconds()*latencyCaptureRate)
	if err := waitForPCM(ctx, collector, targetSamples, captureDone, latencySearchDuration+4*time.Second); err != nil {
		// If enough audio was captured to contain a plausible result, still try
		// correlation before failing only because the final target was short.
		if collector.len() < baseline+latencyCaptureRate {
			return result, fmt.Errorf("insufficient %s audio after latency marker: %w", cfg.ReceiveMode(), err)
		}
	}
	captured := collector.slice(baseline, targetSamples)
	result.SamplesScanned = len(captured)
	peaks, ok := estimateAcousticDelay(captured, markerCapture, latencyCaptureRate)
	result.Delay = peaks.Delay
	result.Correlation = peaks.Best
	result.SecondPeak = peaks.Second
	result.PeakMargin = peaks.Margin
	result.PeakRatio = peaks.Ratio
	if !ok {
		return result, errors.New("acoustic marker correlation could not be evaluated")
	}
	if !peaks.Reliable {
		return result, &LatencyAmbiguousError{
			Best: peaks.Best, Second: peaks.Second, Margin: peaks.Margin, Ratio: peaks.Ratio,
		}
	}
	return result, nil
}

type baichuanLatencyCapture struct {
	receiver  *baichuanaudio.Receiver
	collector *pcmCollector
	done      chan error
	cancel    context.CancelFunc
	once      sync.Once
	wg        sync.WaitGroup
}

func (c *baichuanLatencyCapture) close() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		if c.receiver != nil {
			c.receiver.Close()
		}
		c.wg.Wait()
	})
}

func startBaichuanLatencyCapture(parent context.Context, cfg config.Config, logger *slog.Logger) (*baichuanLatencyCapture, baichuanaudio.Info, error) {
	stream, err := baichuan.ParseStream(config.BaichuanReceiveStream)
	if err != nil {
		return nil, baichuanaudio.Info{}, err
	}
	ctx, cancel := context.WithCancel(parent)
	receiver, err := baichuanaudio.Open(ctx, baichuanaudio.Config{
		Host: cfg.ReolinkHost, Port: cfg.BaichuanPort,
		Username: cfg.ReolinkUsername, Password: cfg.ReolinkPassword,
		Channel: uint8(cfg.NVRChannel), Stream: stream,
		OutputRate: latencyCaptureRate, FFmpegPath: cfg.FFmpegPath(),
		Logger: logger, Debug: cfg.DebugEnabled(),
	})
	if err != nil {
		cancel()
		return nil, baichuanaudio.Info{}, err
	}

	readyTimer := time.NewTimer(8 * time.Second)
	var info baichuanaudio.Info
	select {
	case got, ok := <-receiver.Ready():
		if !readyTimer.Stop() {
			<-readyTimer.C
		}
		if !ok {
			receiver.Close()
			cancel()
			return nil, info, errors.New("Baichuan preview ended before audio became ready")
		}
		info = got
	case recvErr := <-receiver.Done():
		if !readyTimer.Stop() {
			<-readyTimer.C
		}
		receiver.Close()
		cancel()
		if recvErr == nil {
			recvErr = errors.New("Baichuan preview ended before audio became ready")
		}
		return nil, info, recvErr
	case <-readyTimer.C:
		receiver.Close()
		cancel()
		return nil, info, errors.New("timed out waiting for Baichuan preview audio")
	case <-ctx.Done():
		if !readyTimer.Stop() {
			<-readyTimer.C
		}
		receiver.Close()
		cancel()
		return nil, info, ctx.Err()
	}

	capture := &baichuanLatencyCapture{
		receiver: receiver, collector: &pcmCollector{}, done: make(chan error, 1), cancel: cancel,
	}
	capture.wg.Add(1)
	go func() {
		defer capture.wg.Done()
		defer close(capture.done)
		for {
			select {
			case <-ctx.Done():
				capture.done <- ctx.Err()
				return
			case recvErr, ok := <-receiver.Done():
				if !ok || recvErr == nil {
					recvErr = errors.New("Baichuan preview ended")
				}
				capture.done <- recvErr
				return
			case pcm, ok := <-receiver.PCM():
				if !ok {
					capture.done <- errors.New("Baichuan PCM stream ended")
					return
				}
				capture.collector.append(pcm)
			}
		}
	}()
	return capture, info, nil
}

func openBaichuanLatencyCaptureWithRetry(ctx context.Context, cfg config.Config, logger *slog.Logger) (*baichuanLatencyCapture, int, baichuanaudio.Info, error) {
	var lastErr error
	var lastInfo baichuanaudio.Info
	for attempt := 1; attempt <= latencyCaptureAttempts; attempt++ {
		capture, info, err := startBaichuanLatencyCapture(ctx, cfg, logger)
		lastInfo = info
		if err == nil {
			err = waitForPCM(ctx, capture.collector, latencyWarmupSamples, capture.done, 7*time.Second)
			if err == nil {
				err = waitForRealtimePCM(ctx, capture.collector, capture.done)
				if err != nil {
					err = fmt.Errorf("Baichuan latency capture is not pacing close to real time: %w", err)
				}
			}
		}
		if err == nil {
			if logger != nil {
				logger.Info("Baichuan latency capture ready", "attempt", attempt,
					"warmup_samples", capture.collector.len(), "codec", info.Codec,
					"input_sample_rate", info.InputSampleRate, "stream", info.Stream.ShortName(), "channel", info.Channel)
			}
			return capture, attempt, info, nil
		}
		if capture != nil {
			capture.close()
		}
		lastErr = err
		if logger != nil {
			logger.Warn("Baichuan latency capture startup attempt failed", "attempt", attempt, "max_attempts", latencyCaptureAttempts, "error", err)
		}
		if attempt == latencyCaptureAttempts {
			break
		}
		timer := time.NewTimer(latencyCaptureRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, attempt, lastInfo, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, latencyCaptureAttempts, lastInfo, fmt.Errorf("Baichuan latency capture did not become ready after %d attempts: %w", latencyCaptureAttempts, lastErr)
}

func startLatencyCapture(parent context.Context, cfg config.Config, inputURL string) (*latencyCapture, error) {
	ffCtx, cancelFF := context.WithCancel(parent)
	cmd := exec.CommandContext(ffCtx, cfg.FFmpegPath(), buildLatencyCaptureArgs(cfg, inputURL)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancelFF()
		return nil, fmt.Errorf("prepare latency RTSP capture: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancelFF()
		return nil, fmt.Errorf("start latency RTSP capture: %w", err)
	}
	collector := &pcmCollector{}
	done := make(chan error, 1)
	go func() {
		done <- collectPCM16(stdout, collector)
		close(done)
	}()
	return &latencyCapture{cmd: cmd, cancel: cancelFF, collector: collector, done: done, stderr: stderr}, nil
}

func openLatencyCaptureWithRetry(ctx context.Context, cfg config.Config, inputURL string, logger *slog.Logger) (*latencyCapture, int, error) {
	var lastErr error
	for attempt := 1; attempt <= latencyCaptureAttempts; attempt++ {
		capture, err := startLatencyCapture(ctx, cfg, inputURL)
		if err == nil {
			err = waitForPCM(ctx, capture.collector, latencyWarmupSamples, capture.done, 7*time.Second)
			if err == nil {
				err = waitForRealtimePCM(ctx, capture.collector, capture.done)
				if err != nil {
					err = fmt.Errorf("RTSP latency capture is not pacing close to real time: %w", err)
				}
			}
		}
		if err == nil {
			if logger != nil {
				logger.Info("RTSP latency capture ready", "attempt", attempt, "warmup_samples", capture.collector.len())
			}
			return capture, attempt, nil
		}

		if capture != nil {
			// Stop FFmpeg before reading stderr. exec.Cmd writes to the buffer from
			// its own I/O goroutine, so reading it while the process is still alive
			// would introduce a data race in this retry/error path.
			capture.close()
			if msg := strings.TrimSpace(capture.stderrText(cfg)); msg != "" {
				err = fmt.Errorf("%w; ffmpeg: %s", err, msg)
			}
		}
		lastErr = err
		if logger != nil {
			logger.Warn("RTSP latency capture startup attempt failed", "attempt", attempt, "max_attempts", latencyCaptureAttempts, "error", err)
		}
		if attempt == latencyCaptureAttempts {
			break
		}
		timer := time.NewTimer(latencyCaptureRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, attempt, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, latencyCaptureAttempts, fmt.Errorf("RTSP latency capture did not become ready after %d attempts: %w", latencyCaptureAttempts, lastErr)
}

func buildLatencyCaptureArgs(cfg config.Config, inputURL string) []string {
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-timeout", "10000000", "-rtsp_transport", "tcp",
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-i", inputURL, "-map", "0:a:0", "-vn", "-ac", "1",
		"-ar", strconv.Itoa(latencyCaptureRate), "-c:a", "pcm_s16le",
		"-f", "s16le", "pipe:1",
	}
}

func collectPCM16(r io.Reader, collector *pcmCollector) error {
	buf := make([]byte, 4096)
	var carry byte
	haveCarry := false
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := buf[:n]
			if haveCarry {
				joined := make([]byte, n+1)
				joined[0] = carry
				copy(joined[1:], data)
				data = joined
				haveCarry = false
			}
			if len(data)%2 != 0 {
				carry = data[len(data)-1]
				haveCarry = true
				data = data[:len(data)-1]
			}
			samples := make([]int16, len(data)/2)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
			}
			collector.append(samples)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}

func waitForPCM(ctx context.Context, c *pcmCollector, count int, done <-chan error, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c.len() >= count {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			if err == nil {
				return errors.New("audio capture ended")
			}
			return fmt.Errorf("audio capture ended: %w", err)
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %d PCM samples (got %d)", count, c.len())
		case <-ticker.C:
		}
	}
}

func captureTermination(done <-chan error) error {
	select {
	case err, ok := <-done:
		if !ok || err == nil {
			return errors.New("audio capture ended")
		}
		return fmt.Errorf("audio capture ended: %w", err)
	default:
		return nil
	}
}

func waitForRealtimePCM(ctx context.Context, c *pcmCollector, done <-chan error) error {
	const window = 500 * time.Millisecond
	expected := int(window.Seconds() * latencyCaptureRate)
	for attempt := 0; attempt < 5; attempt++ {
		start := c.len()
		timer := time.NewTimer(window)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case err := <-done:
			if !timer.Stop() {
				<-timer.C
			}
			if err != nil {
				return err
			}
			return errors.New("audio capture ended")
		case <-timer.C:
		}
		delta := c.len() - start
		// Allow wide tolerance for scheduler jitter and packetised delivery, but
		// reject startup catch-up bursts that invalidate sample-index timing.
		if delta >= expected/2 && delta <= expected*2 {
			return nil
		}
	}
	return errors.New("audio sample delivery stayed substantially faster/slower than real time")
}

func generateLatencyMarker(rate int) []int16 {
	if rate <= 0 {
		return nil
	}
	totalSamples := int(math.Round(latencyMarkerDuration.Seconds() * float64(rate)))
	if totalSamples < latencyMarkerSymbols*8 {
		return nil
	}
	out := make([]int16, totalSamples)
	fade := int(math.Round(0.006 * float64(rate))) // 6 ms fade on each symbol edge.
	if fade < 1 {
		fade = 1
	}
	for symbolIndex, code := range latencyMarkerCode {
		if int(code) >= len(latencyMarkerFrequencies) {
			continue
		}
		start := int(math.Round(float64(symbolIndex) * latencyMarkerSymbolDuration.Seconds() * float64(rate)))
		end := int(math.Round(float64(symbolIndex+1) * latencyMarkerSymbolDuration.Seconds() * float64(rate)))
		if end > len(out) {
			end = len(out)
		}
		if start >= end {
			continue
		}
		symbolSamples := end - start
		symbolFade := fade
		if symbolFade*2 >= symbolSamples {
			symbolFade = symbolSamples / 4
		}
		freq := latencyMarkerFrequencies[code]
		phaseStep := 2 * math.Pi * freq / float64(rate)
		phase := 0.0
		for i := 0; i < symbolSamples; i++ {
			env := 1.0
			if i < symbolFade {
				x := float64(i) / float64(symbolFade)
				env = math.Sin(x * math.Pi / 2)
				env *= env
			}
			if tail := symbolSamples - 1 - i; tail < symbolFade {
				x := float64(tail) / float64(symbolFade)
				tailEnv := math.Sin(x * math.Pi / 2)
				env *= tailEnv * tailEnv
			}
			out[start+i] = int16(math.Round(latencyMarkerAmplitude * env * math.Sin(phase)))
			phase += phaseStep
		}
	}
	return out
}

func sendPCMThroughBaichuan(ctx context.Context, client *baichuan.Client, session *baichuan.TalkSession, pcm []int16) (markerSendStats, error) {
	var stats markerSendStats
	rate := session.SampleRate()
	blockSamples := session.SamplesPerBlock()
	if rate <= 0 || blockSamples < 2 || blockSamples%2 != 0 {
		return stats, errors.New("invalid Baichuan latency marker profile")
	}
	blockDuration := time.Duration(int64(time.Second) * int64(blockSamples) / int64(rate))
	encoder := &codec.ADPCMEncoder{}
	stats.BlocksExpected = (len(pcm) + blockSamples - 1) / blockSamples
	started := time.Now()
	next := time.Now()
	for offset, blockIndex := 0, 0; offset < len(pcm); offset, blockIndex = offset+blockSamples, blockIndex+1 {
		if blockIndex > 0 {
			next = next.Add(blockDuration)
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return stats, ctx.Err()
			case <-client.Done():
				if !timer.Stop() {
					<-timer.C
				}
				if err := client.Err(); err != nil {
					return stats, err
				}
				return stats, errors.New("Baichuan connection closed during latency marker")
			case <-timer.C:
			}
		}
		block := make([]int16, blockSamples)
		end := offset + blockSamples
		if end > len(pcm) {
			end = len(pcm)
		}
		copy(block, pcm[offset:end])
		encoded, err := encoder.EncodeBlock(block)
		if err != nil {
			return stats, fmt.Errorf("encode acoustic latency marker: %w", err)
		}
		if len(encoded) != session.BytesPerBlock() {
			return stats, fmt.Errorf("acoustic marker ADPCM block size %d, expected %d", len(encoded), session.BytesPerBlock())
		}
		if err := session.WriteADPCMBlock(ctx, encoded); err != nil {
			return stats, fmt.Errorf("send acoustic latency marker block %d/%d: %w", blockIndex+1, stats.BlocksExpected, err)
		}
		stats.BlocksSent++
	}
	stats.MediaDuration = time.Duration(stats.BlocksSent) * blockDuration
	stats.Elapsed = time.Since(started)
	return stats, nil
}

func sendPCMThroughRTSP(ctx context.Context, client *rtsp.Client, bc rtsp.Backchannel, pcm []int16) (markerSendStats, error) {
	var stats markerSendStats
	const blockSamples = 160 // 20 ms at 8 kHz, matching the live RTSP talkback path.
	const blockDuration = 20 * time.Millisecond
	codecName := strings.ToLower(strings.TrimSpace(bc.Codec))
	if codecName != g711.PCMA && codecName != g711.PCMU {
		return stats, fmt.Errorf("unsupported RTSP backchannel codec %q for latency calibration", bc.Codec)
	}
	stats.BlocksExpected = (len(pcm) + blockSamples - 1) / blockSamples
	started := time.Now()
	next := time.Now()
	for offset, blockIndex := 0, 0; offset < len(pcm); offset, blockIndex = offset+blockSamples, blockIndex+1 {
		if blockIndex > 0 {
			next = next.Add(blockDuration)
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return stats, ctx.Err()
			case <-client.Done():
				if !timer.Stop() {
					<-timer.C
				}
				select {
				case err := <-client.WaitError():
					if err != nil {
						return stats, err
					}
				default:
				}
				return stats, errors.New("RTSP backchannel closed during latency marker")
			case <-timer.C:
			}
		}
		block := make([]int16, blockSamples)
		end := offset + blockSamples
		if end > len(pcm) {
			end = len(pcm)
		}
		copy(block, pcm[offset:end])
		payload := g711.EncodePCM(block, codecName)
		if len(payload) != blockSamples {
			return stats, fmt.Errorf("encode RTSP latency marker produced %d bytes, expected %d", len(payload), blockSamples)
		}
		if err := client.WriteAudio(payload, bc.PayloadType); err != nil {
			return stats, fmt.Errorf("send RTSP latency marker frame %d/%d: %w", blockIndex+1, stats.BlocksExpected, err)
		}
		stats.BlocksSent++
	}
	stats.MediaDuration = time.Duration(stats.BlocksSent) * blockDuration
	stats.Elapsed = time.Since(started)
	return stats, nil
}

type acousticPeaks struct {
	Delay    time.Duration
	Best     float64
	Second   float64
	Margin   float64
	Ratio    float64
	Reliable bool
}

// estimateAcousticDelay uses normalized cross-correlation on an 8 kHz
// downsampled first-difference signal. The coded marker occupies 850..2300 Hz,
// so 8 kHz preserves its speech-band structure while keeping the diagnostic
// inexpensive. A second independent peak is measured outside a wide guard
// interval around the winning lag; this prevents a random/noisy local maximum
// near the absolute threshold from being reported as a valid delay.
func estimateAcousticDelay(captured, reference []int16, sampleRate int) (acousticPeaks, bool) {
	var result acousticPeaks
	if sampleRate <= 0 || len(reference) < 32 || len(captured) < len(reference) {
		return result, false
	}
	factor := sampleRate / 8000
	if factor < 1 {
		factor = 1
	}
	capDS := difference(downsampleAverage(captured, factor))
	refDS := difference(downsampleAverage(reference, factor))
	if len(refDS) < 16 || len(capDS) < len(refDS) {
		return result, false
	}
	var refEnergy float64
	for _, v := range refDS {
		refEnergy += v * v
	}
	if refEnergy == 0 {
		return result, false
	}

	window := len(refDS)
	dsRate := float64(sampleRate) / float64(factor)
	maxLag := len(capDS) - window
	if maxLag < 0 {
		return result, false
	}
	// Prefix energy makes normalization O(1) per candidate. The expensive dot
	// product is evaluated first on a 1-ms grid and then refined sample-wise
	// around the two relevant peaks. At the normal 16-kHz capture rate this
	// reduces a 5-s/1-s search from ~260 million to ~33 million multiply-adds
	// without sacrificing the final delay resolution.
	energyPrefix := make([]float64, len(capDS)+1)
	for i, v := range capDS {
		energyPrefix[i+1] = energyPrefix[i] + v*v
	}
	coarseStep := int(math.Round(dsRate * 0.001))
	if coarseStep < 1 {
		coarseStep = 1
	}
	type candidate struct {
		lag   int
		score float64
	}
	candidates := make([]candidate, 0, maxLag/coarseStep+2)
	eval := func(lag int) float64 {
		if lag < 0 || lag > maxLag {
			return 0
		}
		winEnergy := energyPrefix[lag+window] - energyPrefix[lag]
		if winEnergy <= 0 {
			return 0
		}
		var dot float64
		for i, rv := range refDS {
			dot += rv * capDS[lag+i]
		}
		return math.Abs(dot) / math.Sqrt(refEnergy*winEnergy)
	}
	bestCoarse := candidate{}
	lastLag := -1
	for lag := 0; lag <= maxLag; lag += coarseStep {
		score := eval(lag)
		c := candidate{lag: lag, score: score}
		candidates = append(candidates, c)
		if score > bestCoarse.score {
			bestCoarse = c
		}
		lastLag = lag
	}
	if lastLag != maxLag {
		score := eval(maxLag)
		c := candidate{lag: maxLag, score: score}
		candidates = append(candidates, c)
		if score > bestCoarse.score {
			bestCoarse = c
		}
	}
	refine := func(center int, independentFrom int, guard int) candidate {
		best := candidate{lag: center, score: eval(center)}
		from := center - coarseStep
		if from < 0 {
			from = 0
		}
		to := center + coarseStep
		if to > maxLag {
			to = maxLag
		}
		for lag := from; lag <= to; lag++ {
			if independentFrom >= 0 && lag >= independentFrom-guard && lag <= independentFrom+guard {
				continue
			}
			score := eval(lag)
			if score > best.score {
				best = candidate{lag: lag, score: score}
			}
		}
		return best
	}
	best := refine(bestCoarse.lag, -1, 0)
	bestScore, bestLag := best.score, best.lag

	guardSamples := int(math.Round(latencyIndependentPeakGuard.Seconds() * dsRate))
	if guardSamples < 1 {
		guardSamples = 1
	}
	secondCoarse := candidate{}
	for _, c := range candidates {
		if c.lag >= bestLag-guardSamples && c.lag <= bestLag+guardSamples {
			continue
		}
		if c.score > secondCoarse.score {
			secondCoarse = c
		}
	}
	second := secondCoarse
	if secondCoarse.score > 0 {
		second = refine(secondCoarse.lag, bestLag, guardSamples)
	}
	secondScore := second.score
	margin := bestScore - secondScore
	ratio := math.Inf(1)
	if secondScore > 0 {
		ratio = bestScore / secondScore
	}
	reliable := bestScore >= latencyCorrelationFloor && (margin >= latencyPeakMarginFloor || ratio >= latencyPeakRatioFloor)
	delay := time.Duration(float64(bestLag) / dsRate * float64(time.Second))
	result = acousticPeaks{Delay: delay, Best: bestScore, Second: secondScore, Margin: margin, Ratio: ratio, Reliable: reliable}
	return result, true
}

func downsampleAverage(in []int16, factor int) []float64 {
	if factor <= 1 {
		out := make([]float64, len(in))
		for i, v := range in {
			out[i] = float64(v)
		}
		return out
	}
	n := len(in) / factor
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		var sum float64
		base := i * factor
		for j := 0; j < factor; j++ {
			sum += float64(in[base+j])
		}
		out[i] = sum / float64(factor)
	}
	return out
}

func difference(in []float64) []float64 {
	if len(in) < 2 {
		return nil
	}
	out := make([]float64, len(in)-1)
	for i := 1; i < len(in); i++ {
		out[i-1] = in[i] - in[i-1]
	}
	return out
}
