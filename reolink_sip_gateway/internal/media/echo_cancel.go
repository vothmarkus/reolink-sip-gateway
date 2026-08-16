package media

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
)

const (
	aecSampleRate              = 8000
	aecFrameDuration           = 10 * time.Millisecond
	aecFrameSamples            = aecSampleRate / 100 // WebRTC APM consumes 10 ms frames.
	aecSampleDuration          = time.Second / aecSampleRate
	aecTrackerWindow           = 300 * time.Millisecond
	aecTrackerInterval         = 500 * time.Millisecond
	aecTrackerStep             = 1 * time.Millisecond
	aecTrackerExclusion        = 30 * time.Millisecond
	aecTrackerConsistency      = 20 * time.Millisecond
	aecTrackerConfirmations    = 3
	aecTrackerConfirmationAge  = 4 * time.Second
	aecTrackerRecoveryPause    = 1500 * time.Millisecond
	aecHealthLogInterval       = 5 * time.Second
	aecRecentERLEAlpha         = 0.02
	productionAECDelayTracking = false
)

type echoFrameProcessor interface {
	Process(context.Context, []int16, []int16) ([]int16, error)
	Close() error
}

type echoProcessorStatsProvider interface {
	NativeStats() nativeAECStats
}

type aecTimedFrame struct {
	at      time.Time
	samples []int16
}

type echoStats struct {
	CaptureFrames            uint64
	RenderFrames             uint64
	MissingRenderFrames      uint64
	TrackerAttempts          uint64
	TrackerUpdates           uint64
	TrackerSuspensions       uint64
	TrackerSuspendedAttempts uint64
	TrackerCandidateStreak   int
	CurrentDelayMS           int
	BestCorrelation          float64
	SecondCorrelation        float64
	BestCandidateMS          int
	EstimatedERLEDB          float64
	RecentERLEDB             float64
	ERLEFrames               uint64
	ERLELastAgeMS            int64
	Native                   nativeAECStats
}

// echoCanceller performs the long-delay alignment in Go and delegates only the
// short residual acoustic echo path to WebRTC APM. This is important for the
// Reolink path where the measured acoustic round trip is roughly 1.4 seconds:
// the WebRTC filter must not be asked to model a 1.4 s impulse response.
type echoCanceller struct {
	cfg       config.Config
	logger    *slog.Logger
	processor echoFrameProcessor

	mu                     sync.Mutex
	renderHistory          []aecTimedFrame
	captureHistory         []aecTimedFrame
	renderPending          []int16
	renderNextAt           time.Time
	currentDelay           time.Duration
	lastTrack              time.Time
	trackerBest            float64
	trackerSecond          float64
	trackerCandidate       time.Duration
	trackerConfirmations   []time.Duration
	trackerLastConfident   time.Time
	trackingSuspendedUntil time.Time
	lastSuspendLog         time.Time
	stats                  echoStats
	erleInputEnergy        float64
	erleOutputEnergy       float64
	erleRecentInput        float64
	erleRecentOutput       float64
	erleRecentInitialized  bool
	lastERLEAt             time.Time
	lastCaptureAt          time.Time
	lastHealthLog          time.Time
	onStatus               func(echoStats)
	trackingEnabled        bool // tracker implementation remains available for focused tests; production disables it
}

func startEchoCanceller(ctx context.Context, cfg config.Config, logger *slog.Logger) (*echoCanceller, error) {
	proc, err := newNativeAECProcessor(ctx, nativeAECOptions{
		HighPassFilter:        cfg.WebRTCHighPassFilterEnabled,
		NoiseSuppression:      cfg.WebRTCNoiseSuppressionEnabled,
		NoiseSuppressionLevel: config.WebRTCNoiseSuppressionLevel,
	}, logger)
	if err != nil {
		return nil, err
	}
	ec := newProductionEchoCancellerWithProcessor(cfg, logger, proc)
	// Prime and validate the native APM helper before the call is declared
	// media-active. A missing/incompatible runtime therefore fails deterministically
	// during setup rather than after the user has started speaking.
	silence := make([]int16, aecFrameSamples)
	warmCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for i := 0; i < 6; i++ {
		if _, err := proc.Process(warmCtx, silence, silence); err != nil {
			_ = proc.Close()
			return nil, fmt.Errorf("initialize WebRTC echo canceller: %w", err)
		}
	}
	if logger != nil {
		logger.Info("WebRTC native echo cancellation active",
			"engine", "libwebrtc-audio-processing-1",
			"helper", nativeAECHelperBinary,
			"external_delay_alignment", true,
			"apm_stream_delay_ms", 0,
			"sample_rate", aecSampleRate,
			"frame_ms", aecFrameDuration.Milliseconds(),
			"initial_delay_ms", cfg.AECInitialDelayMS,
			"delay_tracking", productionAECDelayTracking,
			"min_delay_ms", cfg.AECMinDelayMS,
			"max_delay_ms", cfg.AECMaxDelayMS,
			"high_pass_filter", cfg.WebRTCHighPassFilterEnabled,
			"noise_suppression", cfg.WebRTCNoiseSuppressionEnabled,
			"noise_suppression_level", config.WebRTCNoiseSuppressionLevel)
	}
	return ec, nil
}

func newEchoCancellerWithProcessor(cfg config.Config, logger *slog.Logger, proc echoFrameProcessor) *echoCanceller {
	delay := time.Duration(cfg.AECInitialDelayMS) * time.Millisecond
	return &echoCanceller{cfg: cfg, logger: logger, processor: proc, currentDelay: delay, trackingEnabled: true}
}

func newProductionEchoCancellerWithProcessor(cfg config.Config, logger *slog.Logger, proc echoFrameProcessor) *echoCanceller {
	ec := newEchoCancellerWithProcessor(cfg, logger, proc)
	ec.trackingEnabled = productionAECDelayTracking
	return ec
}

func (e *echoCanceller) SetStatusCallback(fn func(echoStats)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.onStatus = fn
	e.mu.Unlock()
}

func (e *echoCanceller) Close() error {
	if e == nil || e.processor == nil {
		return nil
	}
	st := e.Stats()
	if e.logger != nil {
		e.logger.Debug("WebRTC echo canceller stopped",
			"capture_frames", st.CaptureFrames,
			"render_frames", st.RenderFrames,
			"missing_render_frames", st.MissingRenderFrames,
			"delay_ms", st.CurrentDelayMS,
			"tracker_attempts", st.TrackerAttempts,
			"tracker_updates", st.TrackerUpdates,
			"tracker_suspensions", st.TrackerSuspensions,
			"tracker_suspended_attempts", st.TrackerSuspendedAttempts,
			"tracker_candidate_streak", st.TrackerCandidateStreak,
			"tracker_correlation", st.BestCorrelation,
			"tracker_second_peak", st.SecondCorrelation,
			"tracker_candidate_ms", st.BestCandidateMS,
			"estimated_erle_db", st.EstimatedERLEDB,
			"recent_erle_db", st.RecentERLEDB,
			"estimated_erle_frames", st.ERLEFrames,
			"erle_last_age_ms", st.ERLELastAgeMS,
			"native_stats_mask", fmt.Sprintf("0x%02x", st.Native.ValidMask),
			"native_erl_db", nativeFloat(st.Native, nativeStatERL, st.Native.EchoReturnLossDB),
			"native_erle_db", nativeFloat(st.Native, nativeStatERLE, st.Native.EchoReturnLossEnhancementDB),
			"native_delay_ms", nativeInt(st.Native, nativeStatDelayMS, st.Native.DelayMS),
			"native_delay_median_ms", nativeInt(st.Native, nativeStatDelayMedianMS, st.Native.DelayMedianMS),
			"native_delay_stddev_ms", nativeInt(st.Native, nativeStatDelayStdDevMS, st.Native.DelayStdDevMS),
			"native_residual_echo_likelihood", nativeFloat(st.Native, nativeStatResidualEchoLikelihood, st.Native.ResidualEchoLikelihood),
			"native_residual_echo_likelihood_recent_max", nativeFloat(st.Native, nativeStatResidualEchoLikelihoodRecentMax, st.Native.ResidualEchoLikelihoodRecentMax),
			"native_divergent_filter_fraction", nativeFloat(st.Native, nativeStatDivergentFilterFraction, st.Native.DivergentFilterFraction))
	}
	return e.processor.Close()
}

// AddRender stores playout-synchronised 8 kHz reference PCM. Since v0.4.2 the
// caller invokes this only after the corresponding RTSP/Baichuan write
// succeeded; Baichuan references are reconstructed from the encoded ADPCM
// block. The timeline therefore includes jitter, inserted silence, FIFO drops
// and transport framing rather than the earlier SIP-arrival clock.
func (e *echoCanceller) AddRender(pcm []int16, now time.Time) {
	if e == nil || len(pcm) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	// Baichuan blocks are typically 64 ms while APM consumes 10 ms frames. Keep
	// a continuous sample clock across normal block boundaries, but rebase after
	// a genuine transport/scheduler discontinuity. A rebase also invalidates any
	// pending delay-tracker vote so a broken render window cannot move alignment.
	rebase := e.renderNextAt.IsZero() || now.Sub(e.renderNextAt) > 120*time.Millisecond || e.renderNextAt.Sub(now) > 80*time.Millisecond
	if rebase {
		initial := e.renderNextAt.IsZero()
		e.renderNextAt = now
		e.renderPending = e.renderPending[:0]
		if !initial && e.trackingEnabled {
			e.trackerConfirmations = e.trackerConfirmations[:0]
			until := now.Add(aecTrackerRecoveryPause)
			if until.After(e.trackingSuspendedUntil) {
				e.trackingSuspendedUntil = until
			}
			e.stats.TrackerSuspensions++
		}
	}
	e.renderPending = append(e.renderPending, pcm...)
	for len(e.renderPending) >= aecFrameSamples {
		frame := append([]int16(nil), e.renderPending[:aecFrameSamples]...)
		e.renderPending = e.renderPending[aecFrameSamples:]
		e.renderHistory = append(e.renderHistory, aecTimedFrame{at: e.renderNextAt, samples: frame})
		e.renderNextAt = e.renderNextAt.Add(aecFrameDuration)
		e.stats.RenderFrames++
	}
	e.pruneRenderLocked(now)
}

// ProcessCapture processes one or more whole 10 ms frames. The live callers use
// 20 ms/160-sample blocks, so their RTP cadence is unchanged by enabling AEC.
func (e *echoCanceller) ProcessCapture(ctx context.Context, pcm []int16, now time.Time) ([]int16, error) {
	if e == nil {
		return append([]int16(nil), pcm...), nil
	}
	if len(pcm) == 0 || len(pcm)%aecFrameSamples != 0 {
		return nil, fmt.Errorf("AEC capture block must contain a whole number of %d-sample/10 ms frames, got %d", aecFrameSamples, len(pcm))
	}
	out := make([]int16, 0, len(pcm))
	frames := len(pcm) / aecFrameSamples
	for i := 0; i < frames; i++ {
		frameAt := now.Add(time.Duration(i) * aecFrameDuration)
		capture := append([]int16(nil), pcm[i*aecFrameSamples:(i+1)*aecFrameSamples]...)

		e.mu.Lock()
		e.captureHistory = append(e.captureHistory, aecTimedFrame{at: frameAt, samples: append([]int16(nil), capture...)})
		e.lastCaptureAt = frameAt
		e.stats.CaptureFrames++
		if e.trackingEnabled && (e.lastTrack.IsZero() || frameAt.Sub(e.lastTrack) >= aecTrackerInterval) {
			e.trackDelayLocked(frameAt)
			e.lastTrack = frameAt
		}
		delay := e.currentDelay
		reference, found := e.referenceAtLocked(frameAt.Add(-delay))
		if !found {
			reference = make([]int16, aecFrameSamples)
			e.stats.MissingRenderFrames++
		}
		e.pruneCaptureLocked(frameAt)
		// Capture media time can intentionally lag the local render/write clock
		// because the Baichuan/AAC smoother carries its own virtual source time.
		// Pruning render history here with the (older) capture clock is safe and
		// merely retains a little extra history.
		e.pruneRenderLocked(frameAt)
		e.mu.Unlock()

		processed, err := e.processor.Process(ctx, reference, capture)
		if err != nil {
			return nil, err
		}
		if len(processed) != aecFrameSamples {
			return nil, fmt.Errorf("AEC processor returned %d samples, expected %d", len(processed), aecFrameSamples)
		}
		e.observeERLE(reference, capture, processed, frameAt)
		e.maybeLogHealth(frameAt)
		out = append(out, processed...)
	}
	return out, nil
}

func (e *echoCanceller) Stats() echoStats {
	if e == nil {
		return echoStats{}
	}
	e.mu.Lock()
	st := e.stats
	st.CurrentDelayMS = int(e.currentDelay / time.Millisecond)
	st.BestCorrelation = e.trackerBest
	st.SecondCorrelation = e.trackerSecond
	st.BestCandidateMS = int(e.trackerCandidate / time.Millisecond)
	st.TrackerCandidateStreak = len(e.trackerConfirmations)
	if e.erleOutputEnergy > 0 && e.erleInputEnergy > e.erleOutputEnergy {
		st.EstimatedERLEDB = 10 * math.Log10(e.erleInputEnergy/e.erleOutputEnergy)
	}
	if e.erleRecentOutput > 0 && e.erleRecentInput > e.erleRecentOutput {
		st.RecentERLEDB = 10 * math.Log10(e.erleRecentInput/e.erleRecentOutput)
	}
	if !e.lastERLEAt.IsZero() && !e.lastCaptureAt.IsZero() && e.lastCaptureAt.After(e.lastERLEAt) {
		st.ERLELastAgeMS = e.lastCaptureAt.Sub(e.lastERLEAt).Milliseconds()
	}
	e.mu.Unlock()
	if provider, ok := e.processor.(echoProcessorStatsProvider); ok {
		st.Native = provider.NativeStats()
	}
	return st
}

func (e *echoCanceller) CurrentDelay() time.Duration {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.currentDelay
}

// SuspendTracking temporarily freezes long-delay adaptation after a local
// capture-timeline discontinuity (hard queue drop, true underrun or media-clock
// rebase). AEC itself keeps processing with the last known-good delay; only the
// estimator is paused so it cannot learn from a deliberately broken window.
func (e *echoCanceller) SuspendTracking(at time.Time, reason string) {
	if e == nil || !e.trackingEnabled {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	e.mu.Lock()
	until := at.Add(aecTrackerRecoveryPause)
	if until.After(e.trackingSuspendedUntil) {
		e.trackingSuspendedUntil = until
	}
	e.trackerConfirmations = e.trackerConfirmations[:0]
	e.stats.TrackerSuspensions++
	logNow := e.logger != nil && (e.lastSuspendLog.IsZero() || at.Sub(e.lastSuspendLog) >= time.Second)
	if logNow {
		e.lastSuspendLog = at
	}
	e.mu.Unlock()
	if logNow {
		e.logger.Debug("AEC delay tracking temporarily suspended", "reason", reason, "resume_after_ms", aecTrackerRecoveryPause.Milliseconds())
	}
}

func (e *echoCanceller) observeERLE(reference, before, after []int16, at time.Time) {
	// This is deliberately labelled an estimate. Only accumulate frames where
	// the aligned render reference is clearly active and the raw capture has a
	// measurable linear relationship to it; near-end double-talk would
	// otherwise make an energy ratio look artificially bad or good.
	if g711.RMSDBFS(reference) < -45 || absNormalizedCorrelation(reference, before) < 0.10 {
		return
	}
	inE := sampleEnergy(before)
	outE := sampleEnergy(after)
	if inE <= 0 || outE <= 0 {
		return
	}
	e.mu.Lock()
	e.erleInputEnergy += inE
	e.erleOutputEnergy += outE
	if !e.erleRecentInitialized {
		e.erleRecentInput = inE
		e.erleRecentOutput = outE
		e.erleRecentInitialized = true
	} else {
		e.erleRecentInput = (1-aecRecentERLEAlpha)*e.erleRecentInput + aecRecentERLEAlpha*inE
		e.erleRecentOutput = (1-aecRecentERLEAlpha)*e.erleRecentOutput + aecRecentERLEAlpha*outE
	}
	e.stats.ERLEFrames++
	e.lastERLEAt = at
	e.mu.Unlock()
}

func (e *echoCanceller) maybeLogHealth(now time.Time) {
	if e == nil || e.logger == nil {
		return
	}
	e.mu.Lock()
	if e.lastHealthLog.IsZero() {
		// Establish the reporting epoch without emitting a misleading all-zero
		// health record on the first capture frame.
		e.lastHealthLog = now
		e.mu.Unlock()
		return
	}
	if now.Sub(e.lastHealthLog) < aecHealthLogInterval {
		e.mu.Unlock()
		return
	}
	e.lastHealthLog = now
	e.mu.Unlock()
	st := e.Stats()
	e.mu.Lock()
	onStatus := e.onStatus
	e.mu.Unlock()
	if onStatus != nil {
		onStatus(st)
	}
	e.logger.Debug("AEC health",
		"delay_ms", st.CurrentDelayMS,
		"candidate_ms", st.BestCandidateMS,
		"correlation", st.BestCorrelation,
		"second_peak", st.SecondCorrelation,
		"candidate_streak", st.TrackerCandidateStreak,
		"tracker_updates", st.TrackerUpdates,
		"tracker_suspensions", st.TrackerSuspensions,
		"missing_render_frames", st.MissingRenderFrames,
		"recent_erle_db", st.RecentERLEDB,
		"erle_frames", st.ERLEFrames,
		"erle_last_age_ms", st.ERLELastAgeMS,
		"native_stats_mask", fmt.Sprintf("0x%02x", st.Native.ValidMask),
		"native_erl_db", nativeFloat(st.Native, nativeStatERL, st.Native.EchoReturnLossDB),
		"native_erle_db", nativeFloat(st.Native, nativeStatERLE, st.Native.EchoReturnLossEnhancementDB),
		"native_delay_ms", nativeInt(st.Native, nativeStatDelayMS, st.Native.DelayMS),
		"native_delay_median_ms", nativeInt(st.Native, nativeStatDelayMedianMS, st.Native.DelayMedianMS),
		"native_delay_stddev_ms", nativeInt(st.Native, nativeStatDelayStdDevMS, st.Native.DelayStdDevMS),
		"native_residual_echo_likelihood", nativeFloat(st.Native, nativeStatResidualEchoLikelihood, st.Native.ResidualEchoLikelihood),
		"native_residual_echo_likelihood_recent_max", nativeFloat(st.Native, nativeStatResidualEchoLikelihoodRecentMax, st.Native.ResidualEchoLikelihoodRecentMax),
		"native_divergent_filter_fraction", nativeFloat(st.Native, nativeStatDivergentFilterFraction, st.Native.DivergentFilterFraction))
}

func nativeFloat(st nativeAECStats, bit uint32, value float64) any {
	if !st.has(bit) {
		return nil
	}
	return value
}

func nativeInt(st nativeAECStats, bit uint32, value int32) any {
	if !st.has(bit) {
		return nil
	}
	return value
}

func (e *echoCanceller) pruneRenderLocked(now time.Time) {
	keep := time.Duration(e.cfg.AECMaxDelayMS)*time.Millisecond + 1200*time.Millisecond
	if keep < 2500*time.Millisecond {
		keep = 2500 * time.Millisecond
	}
	e.renderHistory = trimFramesBefore(e.renderHistory, now.Add(-keep))
}

func (e *echoCanceller) pruneCaptureLocked(now time.Time) {
	// Capture history is keyed to camera media time in Baichuan mode, which can
	// intentionally lag the local render/write clock by the jitter-smoother
	// depth. Never prune it from AddRender's wall-clock domain; doing so would
	// silently shorten the 300 ms tracker window as buffering grows.
	e.captureHistory = trimFramesBefore(e.captureHistory, now.Add(-aecTrackerWindow-100*time.Millisecond))
}

func trimFramesBefore(in []aecTimedFrame, cutoff time.Time) []aecTimedFrame {
	first := 0
	for first < len(in) && in[first].at.Before(cutoff) {
		first++
	}
	if first == 0 {
		return in
	}
	oldLen := len(in)
	newLen := oldLen - first
	copy(in, in[first:])
	for i := newLen; i < oldLen; i++ {
		in[i] = aecTimedFrame{}
	}
	return in[:newLen]
}

func (e *echoCanceller) referenceAtLocked(target time.Time) ([]int16, bool) {
	// APM still consumes exact 10 ms frames, but the frame may begin between two
	// stored 10 ms render blocks. Reconstruct the window on the 8 kHz sample
	// timeline instead of rounding to the nearest render block. This removes the
	// old +/-5 ms quantisation error and lets the external delay tracker refine
	// the long Reolink delay in 1 ms steps (8 samples).
	out := make([]int16, aecFrameSamples)
	if !e.fillReferenceAtLocked(target, out) {
		return nil, false
	}
	return out, true
}

// fillReferenceAtLocked reconstructs an 80-sample window beginning at target.
// Render-history frames are generated on an exact 10 ms media clock between
// rebases. If the requested window crosses a frame boundary, the next frame
// must be contiguous; we deliberately refuse to bridge a timeline rebase.
// dst is caller-owned so the tracker can reuse one scratch buffer without
// allocating for every candidate/capture pair.
func (e *echoCanceller) fillReferenceAtLocked(target time.Time, dst []int16) bool {
	i := e.findReferenceFrameLocked(target, -1)
	return i >= 0 && e.fillReferenceFromFrameLocked(i, target, dst)
}

// findReferenceFrameLocked returns the render-history block containing target.
// hint is the block used for the previous capture frame. Capture frames in a
// tracker window advance by exactly 10 ms, so the common case is hint+1 and
// avoids rescanning the full render history for every one of ~700 delay
// candidates. A reverse-search fallback preserves correctness across rebases.
func (e *echoCanceller) findReferenceFrameLocked(target time.Time, hint int) int {
	contains := func(i int) bool {
		if i < 0 || i >= len(e.renderHistory) {
			return false
		}
		f := e.renderHistory[i]
		return len(f.samples) >= aecFrameSamples && !target.Before(f.at) && target.Before(f.at.Add(aecFrameDuration))
	}
	if hint >= 0 {
		// Normal continuous media-clock progression.
		if contains(hint + 1) {
			return hint + 1
		}
		// Tolerate repeated/small-offset lookups without giving up the hint.
		if contains(hint) {
			return hint
		}
		if contains(hint + 2) {
			return hint + 2
		}
	}
	for i := len(e.renderHistory) - 1; i >= 0; i-- {
		if contains(i) {
			return i
		}
	}
	return -1
}

func (e *echoCanceller) fillReferenceFromFrameLocked(i int, target time.Time, dst []int16) bool {
	if len(dst) < aecFrameSamples || i < 0 || i >= len(e.renderHistory) {
		return false
	}
	frame := e.renderHistory[i]
	if len(frame.samples) < aecFrameSamples || target.Before(frame.at) || !target.Before(frame.at.Add(aecFrameDuration)) {
		return false
	}

	delta := target.Sub(frame.at)
	// Round to the closest real 8 kHz sample. The target originates from
	// millisecond delay candidates, so this is exact for the 1 ms tracker;
	// the tolerance also accommodates sub-millisecond media-clock origins.
	offset := int((delta + aecSampleDuration/2) / aecSampleDuration)
	if offset < 0 || offset > aecFrameSamples {
		return false
	}
	actual := frame.at.Add(time.Duration(offset) * aecSampleDuration)
	gridErr := actual.Sub(target)
	if gridErr < 0 {
		gridErr = -gridErr
	}
	if gridErr > aecSampleDuration/2 {
		return false
	}

	if offset == aecFrameSamples {
		// Rounding landed on the first sample of the following block. This can
		// happen when two media-clock origins differ by a fraction of a sample.
		// Accept it only when the following block is genuinely contiguous.
		if i+1 >= len(e.renderHistory) {
			return false
		}
		next := e.renderHistory[i+1]
		if len(next.samples) < aecFrameSamples {
			return false
		}
		continuityErr := next.at.Sub(frame.at.Add(aecFrameDuration))
		if continuityErr < 0 {
			continuityErr = -continuityErr
		}
		if continuityErr > aecSampleDuration/2 {
			return false
		}
		copy(dst, next.samples[:aecFrameSamples])
		return true
	}
	copied := copy(dst, frame.samples[offset:aecFrameSamples])
	if copied == aecFrameSamples {
		return true
	}
	if i+1 >= len(e.renderHistory) {
		return false
	}
	next := e.renderHistory[i+1]
	if len(next.samples) < aecFrameSamples {
		return false
	}
	expectedNext := frame.at.Add(aecFrameDuration)
	continuityErr := next.at.Sub(expectedNext)
	if continuityErr < 0 {
		continuityErr = -continuityErr
	}
	if continuityErr > aecSampleDuration/2 {
		return false
	}
	copy(dst[copied:], next.samples[:aecFrameSamples-copied])
	return true
}

func (e *echoCanceller) trackDelayLocked(now time.Time) {
	e.stats.TrackerAttempts++
	if now.Before(e.trackingSuspendedUntil) {
		e.stats.TrackerSuspendedAttempts++
		return
	}
	cutoff := now.Add(-aecTrackerWindow)
	var captures []aecTimedFrame
	for _, f := range e.captureHistory {
		if !f.at.Before(cutoff) {
			captures = append(captures, f)
		}
	}
	if len(captures) < 20 {
		return
	}

	minDelay := time.Duration(e.cfg.AECMinDelayMS) * time.Millisecond
	maxDelay := time.Duration(e.cfg.AECMaxDelayMS) * time.Millisecond
	type candidate struct {
		delay time.Duration
		corr  float64
	}
	// The old tracker evaluated only whole 10 ms APM-frame offsets. That can miss
	// a narrow correlation peak completely when the real path is, for example,
	// 1503 ms. Scan the configured long-delay range at 1 ms resolution instead.
	// At 8 kHz each step is exactly eight samples and fillReferenceAtLocked()
	// reconstructs the corresponding 10 ms render window without resampling.
	candidates := make([]candidate, 0, int((maxDelay-minDelay)/aecTrackerStep)+1)
	for d := minDelay; d <= maxDelay; d += aecTrackerStep {
		corr, pairs := e.windowCorrelationLocked(captures, d)
		if pairs >= 16 {
			candidates = append(candidates, candidate{delay: d, corr: corr})
		}
	}
	if len(candidates) == 0 {
		return
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.corr > best.corr {
			best = c
		}
	}
	second := 0.0
	for _, c := range candidates {
		dd := c.delay - best.delay
		if dd < 0 {
			dd = -dd
		}
		if dd <= aecTrackerExclusion {
			continue
		}
		if c.corr > second {
			second = c.corr
		}
	}
	e.trackerBest = best.corr
	e.trackerSecond = second
	e.trackerCandidate = best.delay

	margin := best.corr - second
	ratio := math.Inf(1)
	if second > 1e-6 {
		ratio = best.corr / second
	}
	// Real speech produces much weaker correlations than the coded self-test.
	// Keep the amplitude threshold attainable, but require a clearly unique peak
	// and, unlike v0.4.1, three mutually consistent confident observations before
	// moving the long-delay alignment.
	if best.corr < 0.20 || margin < 0.05 || ratio < 1.30 {
		if !e.trackerLastConfident.IsZero() && now.Sub(e.trackerLastConfident) > aecTrackerConfirmationAge {
			e.trackerConfirmations = e.trackerConfirmations[:0]
		}
		return
	}

	if e.trackerLastConfident.IsZero() || now.Sub(e.trackerLastConfident) > aecTrackerConfirmationAge {
		e.trackerConfirmations = e.trackerConfirmations[:0]
	}
	e.trackerLastConfident = now
	if len(e.trackerConfirmations) > 0 {
		center := medianDuration(e.trackerConfirmations)
		d := best.delay - center
		if d < 0 {
			d = -d
		}
		if d > aecTrackerConsistency {
			e.trackerConfirmations = e.trackerConfirmations[:0]
		}
	}
	e.trackerConfirmations = append(e.trackerConfirmations, best.delay)
	if len(e.trackerConfirmations) < aecTrackerConfirmations {
		return
	}
	confirmed := medianDuration(e.trackerConfirmations)
	// Retain the two newest confirmations so another consistent observation can
	// continue a real gradual move, while a jump to a new peak still needs three.
	if len(e.trackerConfirmations) > 2 {
		e.trackerConfirmations = append(e.trackerConfirmations[:0], e.trackerConfirmations[len(e.trackerConfirmations)-2:]...)
	}

	old := e.currentDelay
	delta := confirmed - old
	maxStep := 30 * time.Millisecond
	if delta > maxStep {
		delta = maxStep
	} else if delta < -maxStep {
		delta = -maxStep
	}
	next := old + delta
	next = (next + aecTrackerStep/2) / aecTrackerStep * aecTrackerStep
	if next < minDelay {
		next = minDelay
	} else if next > maxDelay {
		next = maxDelay
	}
	if next == old {
		return
	}
	e.currentDelay = next
	e.stats.TrackerUpdates++
	if e.logger != nil {
		e.logger.Debug("AEC delay tracker updated",
			"old_delay_ms", old.Milliseconds(),
			"new_delay_ms", next.Milliseconds(),
			"candidate_ms", best.delay.Milliseconds(),
			"confirmed_median_ms", confirmed.Milliseconds(),
			"correlation", best.corr,
			"second_peak", second,
			"peak_ratio", ratio)
	}
}

func medianDuration(v []time.Duration) time.Duration {
	if len(v) == 0 {
		return 0
	}
	copyV := append([]time.Duration(nil), v...)
	for i := 1; i < len(copyV); i++ {
		for j := i; j > 0 && copyV[j] < copyV[j-1]; j-- {
			copyV[j], copyV[j-1] = copyV[j-1], copyV[j]
		}
	}
	return copyV[len(copyV)/2]
}

func (e *echoCanceller) windowCorrelationLocked(captures []aecTimedFrame, delay time.Duration) (float64, int) {
	var sumX, sumY, sumXY, sumXX, sumYY float64
	pairs := 0
	n := 0
	var reference [aecFrameSamples]int16
	hint := -1
	for _, cf := range captures {
		target := cf.at.Add(-delay)
		i := e.findReferenceFrameLocked(target, hint)
		if i < 0 || !e.fillReferenceFromFrameLocked(i, target, reference[:]) {
			hint = -1
			continue
		}
		hint = i
		pairs++
		for i := 0; i < aecFrameSamples; i++ {
			x := float64(reference[i])
			y := float64(cf.samples[i])
			sumX += x
			sumY += y
			sumXY += x * y
			sumXX += x * x
			sumYY += y * y
			n++
		}
	}
	if pairs == 0 || n == 0 {
		return 0, pairs
	}
	return zeroMeanCorrelation(sumX, sumY, sumXY, sumXX, sumYY, n), pairs
}

func absNormalizedCorrelation(a, b []int16) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var sumX, sumY, sumXY, sumXX, sumYY float64
	for i := range a {
		x := float64(a[i])
		y := float64(b[i])
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
		sumYY += y * y
	}
	return zeroMeanCorrelation(sumX, sumY, sumXY, sumXX, sumYY, len(a))
}

func zeroMeanCorrelation(sumX, sumY, sumXY, sumXX, sumYY float64, n int) float64 {
	if n <= 1 {
		return 0
	}
	nf := float64(n)
	cov := sumXY - sumX*sumY/nf
	varX := sumXX - sumX*sumX/nf
	varY := sumYY - sumY*sumY/nf
	if varX <= 0 || varY <= 0 {
		return 0
	}
	return math.Abs(cov / math.Sqrt(varX*varY))
}

func sampleEnergy(pcm []int16) float64 {
	var e float64
	for _, s := range pcm {
		v := float64(s)
		e += v * v
	}
	return e
}
