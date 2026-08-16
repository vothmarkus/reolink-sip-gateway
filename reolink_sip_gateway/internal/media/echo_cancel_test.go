package media

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

type recordingAECProcessor struct {
	refs     [][]int16
	subtract bool
}

func (p *recordingAECProcessor) Process(_ context.Context, ref, capture []int16) ([]int16, error) {
	p.refs = append(p.refs, append([]int16(nil), ref...))
	out := append([]int16(nil), capture...)
	if p.subtract {
		for i := range out {
			v := int32(out[i]) - int32(ref[i])
			if v > math.MaxInt16 {
				v = math.MaxInt16
			}
			if v < math.MinInt16 {
				v = math.MinInt16
			}
			out[i] = int16(v)
		}
	}
	return out, nil
}
func (p *recordingAECProcessor) Close() error { return nil }

func TestEchoCancellerAlignsLongFixedDelayBeforeProcessor(t *testing.T) {
	cfg := config.Defaults()
	cfg.AECInitialDelayMS = 1400
	proc := &recordingAECProcessor{}
	ec := newEchoCancellerWithProcessor(cfg, nil, proc)
	base := time.Unix(1000, 0)
	render := make([]int16, 160)
	for i := range render {
		render[i] = int16(1000 + i)
	}
	ec.AddRender(render, base)

	capture := make([]int16, 160)
	if _, err := ec.ProcessCapture(context.Background(), capture, base.Add(1400*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if len(proc.refs) != 2 {
		t.Fatalf("processor frames=%d want 2", len(proc.refs))
	}
	for i := 0; i < 80; i++ {
		if proc.refs[0][i] != render[i] {
			t.Fatalf("first ref[%d]=%d want %d", i, proc.refs[0][i], render[i])
		}
		if proc.refs[1][i] != render[80+i] {
			t.Fatalf("second ref[%d]=%d want %d", i, proc.refs[1][i], render[80+i])
		}
	}
}

func TestEchoCancellerProducesSilenceReferenceWhenHistoryMissing(t *testing.T) {
	cfg := config.Defaults()
	proc := &recordingAECProcessor{}
	ec := newEchoCancellerWithProcessor(cfg, nil, proc)
	if _, err := ec.ProcessCapture(context.Background(), make([]int16, 80), time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if len(proc.refs) != 1 {
		t.Fatalf("refs=%d", len(proc.refs))
	}
	for _, s := range proc.refs[0] {
		if s != 0 {
			t.Fatalf("missing render reference not silent: %d", s)
		}
	}
	if ec.Stats().MissingRenderFrames != 1 {
		t.Fatalf("missing frames=%d", ec.Stats().MissingRenderFrames)
	}
}

func TestProductionEchoCancellerKeepsStartupDelayFixed(t *testing.T) {
	cfg := config.Defaults()
	cfg.AECInitialDelayMS = 1400
	cfg.AECMinDelayMS = 1100
	cfg.AECMaxDelayMS = 1800
	proc := &recordingAECProcessor{}
	ec := newProductionEchoCancellerWithProcessor(cfg, nil, proc)
	if ec.trackingEnabled {
		t.Fatal("production Go delay tracker must be disabled")
	}

	base := time.Unix(1100, 0)
	actualDelayFrames := 160 // deliberately differs by +200 ms from startup alignment.
	history := make([][]int16, 0, 500)
	seed := uint32(0x19a27f31)
	for frame := 0; frame < 420; frame++ {
		samples := make([]int16, aecFrameSamples)
		for i := range samples {
			seed = seed*1664525 + 1013904223
			samples[i] = int16(seed >> 16)
		}
		history = append(history, samples)
		now := base.Add(time.Duration(frame) * aecFrameDuration)
		ec.AddRender(samples, now)
		capture := make([]int16, aecFrameSamples)
		if frame >= actualDelayFrames {
			copy(capture, history[frame-actualDelayFrames])
		}
		if _, err := ec.ProcessCapture(context.Background(), capture, now); err != nil {
			t.Fatal(err)
		}
	}

	st := ec.Stats()
	if got := ec.CurrentDelay(); got != 1400*time.Millisecond {
		t.Fatalf("production AEC moved calibrated delay to %s; want fixed 1.4s", got)
	}
	if st.TrackerAttempts != 0 || st.TrackerUpdates != 0 {
		t.Fatalf("production tracker unexpectedly ran: attempts=%d updates=%d", st.TrackerAttempts, st.TrackerUpdates)
	}
}

func TestEchoDelayTrackerConvergesInsideBounds(t *testing.T) {
	cfg := config.Defaults()
	cfg.AECInitialDelayMS = 1400
	cfg.AECMinDelayMS = 1200
	cfg.AECMaxDelayMS = 1700
	proc := &recordingAECProcessor{}
	ec := newEchoCancellerWithProcessor(cfg, nil, proc)
	base := time.Unix(1000, 0)
	actualDelayFrames := 150 // 1500 ms
	history := make([][]int16, 0, 500)
	seed := uint32(0x12345678)
	for frame := 0; frame < 420; frame++ {
		samples := make([]int16, 80)
		for i := range samples {
			seed = seed*1664525 + 1013904223
			samples[i] = int16(int32(seed>>16) / 2)
		}
		history = append(history, samples)
		now := base.Add(time.Duration(frame) * 10 * time.Millisecond)
		ec.AddRender(samples, now)
		capture := make([]int16, 80)
		if frame >= actualDelayFrames {
			copy(capture, history[frame-actualDelayFrames])
		}
		if _, err := ec.ProcessCapture(context.Background(), capture, now); err != nil {
			t.Fatal(err)
		}
	}
	got := ec.CurrentDelay()
	if got < 1470*time.Millisecond || got > 1530*time.Millisecond {
		t.Fatalf("tracker delay=%s, expected convergence near 1.5s; stats=%+v", got, ec.Stats())
	}
	if ec.Stats().TrackerUpdates == 0 || ec.Stats().BestCorrelation < 0.9 {
		t.Fatalf("tracker did not obtain confident updates: %+v", ec.Stats())
	}
}

func TestEchoReferenceReconstructsSubFrameWindow(t *testing.T) {
	cfg := config.Defaults()
	ec := newEchoCancellerWithProcessor(cfg, nil, &recordingAECProcessor{})
	base := time.Unix(1500, 0)
	render := make([]int16, 160)
	for i := range render {
		render[i] = int16(i + 1)
	}
	ec.AddRender(render, base)

	ec.mu.Lock()
	ref, ok := ec.referenceAtLocked(base.Add(3 * time.Millisecond))
	ec.mu.Unlock()
	if !ok {
		t.Fatal("sub-frame reference was not reconstructed")
	}
	// 3 ms at 8 kHz = 24 samples. The 80-sample APM frame must cross the
	// stored 10 ms block boundary without losing continuity.
	for i := range ref {
		want := render[24+i]
		if ref[i] != want {
			t.Fatalf("ref[%d]=%d want %d", i, ref[i], want)
		}
	}
}

func TestEchoDelayTrackerRefinesToOneMillisecond(t *testing.T) {
	cfg := config.Defaults()
	cfg.AECInitialDelayMS = 1450
	cfg.AECMinDelayMS = 1100
	cfg.AECMaxDelayMS = 1800
	ec := newEchoCancellerWithProcessor(cfg, nil, &recordingAECProcessor{})
	base := time.Unix(2000, 0)

	// Build a continuous, non-periodic render timeline independently from the
	// tracker implementation so a 1503 ms echo has a unique 1 ms optimum.
	const renderFrames = 500
	flat := make([]int16, renderFrames*aecFrameSamples)
	seed := uint32(0x7a31b42d)
	for i := range flat {
		seed = seed*1664525 + 1013904223
		flat[i] = int16(seed >> 16)
	}
	for f := 0; f < renderFrames; f++ {
		start := f * aecFrameSamples
		ec.AddRender(flat[start:start+aecFrameSamples], base.Add(time.Duration(f)*aecFrameDuration))
	}

	actualDelay := 1503 * time.Millisecond
	now := base.Add(4 * time.Second)
	ec.mu.Lock()
	for i := 0; i < 30; i++ {
		at := now.Add(time.Duration(i-29) * aecFrameDuration)
		target := at.Add(-actualDelay)
		startSample := int(target.Sub(base) / aecSampleDuration)
		if startSample < 0 || startSample+aecFrameSamples > len(flat) {
			ec.mu.Unlock()
			t.Fatalf("test source index out of range: %d", startSample)
		}
		capture := append([]int16(nil), flat[startSample:startSample+aecFrameSamples]...)
		ec.captureHistory = append(ec.captureHistory, aecTimedFrame{at: at, samples: capture})
	}
	if corr, pairs := ec.windowCorrelationLocked(ec.captureHistory, actualDelay); corr < 0.99 {
		ec.mu.Unlock()
		t.Fatalf("synthetic fine-delay fixture corr=%f pairs=%d want ~1", corr, pairs)
	}
	// Three consistent observations authorize movement. The first confirmed
	// update is capped at +30 ms (1450 -> 1480); the retained confirmations let
	// the next observation finish the residual move to 1503 ms.
	for i := 0; i < 4; i++ {
		ec.trackDelayLocked(now)
	}
	ec.mu.Unlock()

	if got := ec.CurrentDelay(); got != actualDelay {
		t.Fatalf("fine tracker delay=%s want %s; stats=%+v", got, actualDelay, ec.Stats())
	}
	if got := ec.Stats().BestCandidateMS; got != 1503 {
		t.Fatalf("fine tracker candidate=%dms want 1503ms", got)
	}
}

func TestEchoCancellerEstimatedERLEForSyntheticEcho(t *testing.T) {
	cfg := config.Defaults()
	cfg.AECInitialDelayMS = 1400
	proc := &recordingAECProcessor{subtract: true}
	ec := newEchoCancellerWithProcessor(cfg, nil, proc)
	base := time.Unix(1000, 0)
	render := make([]int16, 160)
	for i := range render {
		render[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/8000))
	}
	ec.AddRender(render, base)
	capture := append([]int16(nil), render...)
	out, err := ec.ProcessCapture(context.Background(), capture, base.Add(1400*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range out {
		if s != 0 {
			t.Fatalf("synthetic perfect canceller left sample %d", s)
		}
	}
	// Output energy is exactly zero, so the conservative metric intentionally
	// does not divide by zero; processing itself is what this test verifies.
}

func TestBuildNativeAECArgsKeepsSpeechFiltersSelectable(t *testing.T) {
	args := buildNativeAECArgs(nativeAECOptions{HighPassFilter: true, NoiseSuppression: true, NoiseSuppressionLevel: "very-high"})
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	for _, want := range []string{"--high-pass=1", "--noise-suppression=1", "--noise-level=very-high"} {
		if !containsWordish(joined, want) {
			t.Fatalf("native AEC args missing %q: %s", want, joined)
		}
	}
	for _, retired := range []string{"echo-suppression", "delay-agnostic", "extended-filter", "gain-control"} {
		if containsWordish(joined, retired) {
			t.Fatalf("native AEC args unexpectedly contain retired GStreamer setting %q: %s", retired, joined)
		}
	}
}

func TestBuildNativeAECArgsNormalizesNoiseLevel(t *testing.T) {
	args := buildNativeAECArgs(nativeAECOptions{HighPassFilter: false, NoiseSuppression: false, NoiseSuppressionLevel: "bogus"})
	joined := ""
	for _, a := range args {
		joined += a + " "
	}
	for _, want := range []string{"--high-pass=0", "--noise-suppression=0", "--noise-level=moderate"} {
		if !containsWordish(joined, want) {
			t.Fatalf("native AEC args missing normalized setting %q: %s", want, joined)
		}
	}
}

func containsWordish(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestNativeAECProcessorPipeProtocolWithFakeChild(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/fake-native-aec"
	script := `#!/usr/bin/python3
import struct,sys
REQ=324

def readn(n):
    out=b''
    while len(out)<n:
        b=sys.stdin.buffer.read(n-len(out))
        if not b:
            return None
        out+=b
    return out
while True:
    req=readn(REQ)
    if req is None:
        break
    if req[:4] != b'AEC1':
        sys.exit(7)
    capture=req[164:324]
    mask=0xff
    reply=(b'AER1'+struct.pack('<iI5d3i',0,mask,11.5,22.25,0.01,0.2,0.3,0,0,0)+capture)
    sys.stdout.buffer.write(reply)
    sys.stdout.buffer.flush()
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := newNativeAECProcessorWithPath(ctx, path, nativeAECOptions{HighPassFilter: true, NoiseSuppression: true, NoiseSuppressionLevel: "moderate"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ref := make([]int16, 80)
	cap := make([]int16, 80)
	for i := range cap {
		cap[i] = int16(i*113 - 3000)
	}
	out, err := p.Process(ctx, ref, cap)
	if err != nil {
		t.Fatal(err)
	}
	for i := range cap {
		if out[i] != cap[i] {
			t.Fatalf("out[%d]=%d want %d", i, out[i], cap[i])
		}
	}
	st := p.NativeStats()
	if st.ValidMask != 0xff || math.Abs(st.EchoReturnLossDB-11.5) > 1e-9 || math.Abs(st.EchoReturnLossEnhancementDB-22.25) > 1e-9 || math.Abs(st.ResidualEchoLikelihood-0.2) > 1e-9 {
		t.Fatalf("unexpected native AEC stats: %+v", st)
	}
}

func TestNativeAECProcessorDrainsFinalReplyBeforeChildExit(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/one-shot-native-aec"
	script := `#!/usr/bin/python3
import struct,sys
req=sys.stdin.buffer.read(324)
if len(req) != 324 or req[:4] != b'AEC1':
    sys.exit(7)
capture=req[164:324]
reply=b'AER1'+struct.pack('<iI5d3i',0,0xff,7.0,13.0,0.02,0.1,0.15,0,0,0)+capture
sys.stdout.buffer.write(reply)
sys.stdout.buffer.flush()
sys.exit(0)
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := newNativeAECProcessorWithPath(ctx, path, nativeAECOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	capture := make([]int16, aecFrameSamples)
	for i := range capture {
		capture[i] = int16(1000 - i*17)
	}
	out, err := p.Process(ctx, make([]int16, aecFrameSamples), capture)
	if err != nil {
		t.Fatalf("lost final helper reply during immediate child exit: %v", err)
	}
	for i := range capture {
		if out[i] != capture[i] {
			t.Fatalf("out[%d]=%d want %d", i, out[i], capture[i])
		}
	}
}

func TestNativeAECProcessorPreservesFastChildDiagnostic(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/fast-failing-native-aec"
	script := "#!/bin/sh\necho 'synthetic native AEC failure' >&2\nexit 9\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, err := newNativeAECProcessorWithPath(ctx, path, nativeAECOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	_, err = p.Process(ctx, make([]int16, aecFrameSamples), make([]int16, aecFrameSamples))
	if err == nil || !containsWordish(err.Error(), "synthetic native AEC failure") {
		t.Fatalf("fast helper diagnostic missing: %v", err)
	}
}

func TestNativeAECResponseRejectsNonzeroStatus(t *testing.T) {
	buf := encodeNativeAECResponse(-7, nativeAECStats{}, make([]int16, aecFrameSamples))
	if _, err := decodeNativeAECResponse(buf); err == nil || !containsWordish(err.Error(), "status -7") {
		t.Fatalf("expected helper status error, got %v", err)
	}
}

func TestEchoRenderPruningDoesNotShortenVirtualCaptureWindow(t *testing.T) {
	cfg := config.Defaults()
	ec := newEchoCancellerWithProcessor(cfg, nil, &recordingAECProcessor{})
	base := time.Unix(6000, 0)

	// Camera media time intentionally trails local render/write time by 180 ms,
	// representative of the v0.4.2 Baichuan burst smoother. Populate a full
	// 300 ms capture tracker window first.
	ec.mu.Lock()
	for i := 0; i < 30; i++ {
		at := base.Add(time.Duration(i) * 10 * time.Millisecond)
		ec.captureHistory = append(ec.captureHistory, aecTimedFrame{at: at, samples: make([]int16, aecFrameSamples)})
	}
	ec.mu.Unlock()

	// A render write at the corresponding newer wall clock must not prune the
	// capture history using that different clock domain.
	ec.AddRender(make([]int16, aecFrameSamples), base.Add(470*time.Millisecond))
	ec.mu.Lock()
	got := len(ec.captureHistory)
	ec.mu.Unlock()
	if got != 30 {
		t.Fatalf("render pruning shortened virtual capture history to %d frames, want 30", got)
	}
}

func TestZeroMeanCorrelationRejectsDCOnlyFrames(t *testing.T) {
	a := make([]int16, 80)
	b := make([]int16, 80)
	for i := range a {
		a[i] = 1200
		b[i] = 900
	}
	if got := absNormalizedCorrelation(a, b); got != 0 {
		t.Fatalf("DC-only correlation=%f want 0", got)
	}
}

func TestEchoDelayTrackerDoesNotChaseUnrelatedAudio(t *testing.T) {
	cfg := config.Defaults()
	cfg.AECInitialDelayMS = 1400
	cfg.AECMinDelayMS = 1100
	cfg.AECMaxDelayMS = 1800
	proc := &recordingAECProcessor{}
	ec := newEchoCancellerWithProcessor(cfg, nil, proc)
	base := time.Unix(1000, 0)
	renderSeed := uint32(0x1234abcd)
	captureSeed := uint32(0x87654321)
	for frame := 0; frame < 400; frame++ {
		render := make([]int16, 80)
		capture := make([]int16, 80)
		for i := range render {
			renderSeed = renderSeed*1664525 + 1013904223
			captureSeed = captureSeed*22695477 + 1
			render[i] = int16(renderSeed >> 16)
			capture[i] = int16(captureSeed >> 16)
		}
		now := base.Add(time.Duration(frame) * 10 * time.Millisecond)
		ec.AddRender(render, now)
		if _, err := ec.ProcessCapture(context.Background(), capture, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := ec.CurrentDelay(); got != 1400*time.Millisecond {
		t.Fatalf("uncorrelated audio moved delay to %s", got)
	}
}

func TestEchoDelayTrackerRequiresThreeConsistentConfidentCandidates(t *testing.T) {
	cfg := config.Defaults()
	cfg.AECInitialDelayMS = 1450
	cfg.AECMinDelayMS = 1100
	cfg.AECMaxDelayMS = 1800
	ec := newEchoCancellerWithProcessor(cfg, nil, &recordingAECProcessor{})
	base := time.Unix(3000, 0)
	seed := uint32(0x52a1b33f)
	render := make([][]int16, 420)
	for f := range render {
		frame := make([]int16, 80)
		for i := range frame {
			seed = seed*1664525 + 1013904223
			frame[i] = int16(seed >> 16)
		}
		render[f] = frame
		ec.AddRender(frame, base.Add(time.Duration(f)*10*time.Millisecond))
	}
	// Build one 300 ms capture window with a very clear 1500 ms echo.
	now := base.Add(4 * time.Second)
	for i := 0; i < 30; i++ {
		at := now.Add(time.Duration(i-29) * 10 * time.Millisecond)
		renderIndex := int(at.Sub(base)/(10*time.Millisecond)) - 150
		ec.captureHistory = append(ec.captureHistory, aecTimedFrame{at: at, samples: append([]int16(nil), render[renderIndex]...)})
	}
	for attempt := 1; attempt <= 2; attempt++ {
		ec.mu.Lock()
		ec.trackDelayLocked(now)
		ec.mu.Unlock()
		if got := ec.CurrentDelay(); got != 1450*time.Millisecond {
			t.Fatalf("attempt %d moved delay prematurely to %s", attempt, got)
		}
	}
	ec.mu.Lock()
	ec.trackDelayLocked(now)
	ec.mu.Unlock()
	if got := ec.CurrentDelay(); got != 1480*time.Millisecond {
		t.Fatalf("third consistent candidate moved delay to %s, want 1480ms", got)
	}
	if ec.Stats().TrackerUpdates != 1 {
		t.Fatalf("tracker updates=%d want 1", ec.Stats().TrackerUpdates)
	}
}

func TestEchoDelayTrackerSuspendsAfterTimelineDiscontinuity(t *testing.T) {
	cfg := config.Defaults()
	ec := newEchoCancellerWithProcessor(cfg, nil, &recordingAECProcessor{})
	base := time.Unix(4000, 0)
	ec.SuspendTracking(base, "test discontinuity")
	ec.mu.Lock()
	ec.trackDelayLocked(base.Add(500 * time.Millisecond))
	ec.mu.Unlock()
	st := ec.Stats()
	if st.TrackerSuspensions != 1 || st.TrackerSuspendedAttempts != 1 {
		t.Fatalf("unexpected suspension stats: %+v", st)
	}
}

func TestNativeAECStatBitAssignmentsMatchWireProtocol(t *testing.T) {
	got := []uint32{
		nativeStatERL,
		nativeStatERLE,
		nativeStatDivergentFilterFraction,
		nativeStatResidualEchoLikelihood,
		nativeStatResidualEchoLikelihoodRecentMax,
		nativeStatDelayMS,
		nativeStatDelayMedianMS,
		nativeStatDelayStdDevMS,
	}
	for i, bit := range got {
		want := uint32(1) << i
		if bit != want {
			t.Fatalf("native statistic bit %d = 0x%x, want 0x%x", i, bit, want)
		}
	}
}

func TestNativeAECStatMaskDecodesAllFields(t *testing.T) {
	const all uint32 = (1 << 8) - 1
	input := nativeAECStats{
		ValidMask:                       all,
		EchoReturnLossDB:                1.25,
		EchoReturnLossEnhancementDB:     2.5,
		DivergentFilterFraction:         0.125,
		ResidualEchoLikelihood:          0.25,
		ResidualEchoLikelihoodRecentMax: 0.5,
		DelayMS:                         13,
		DelayMedianMS:                   17,
		DelayStdDevMS:                   3,
	}
	pcm := make([]int16, aecFrameSamples)
	pcm[0], pcm[len(pcm)-1] = -1234, 2345
	resp, err := decodeNativeAECResponse(encodeNativeAECResponse(0, input, pcm))
	if err != nil {
		t.Fatal(err)
	}
	if resp.stats != input {
		t.Fatalf("decoded stats = %#v, want %#v", resp.stats, input)
	}
	if resp.pcm[0] != -1234 || resp.pcm[len(resp.pcm)-1] != 2345 {
		t.Fatalf("PCM roundtrip failed: first=%d last=%d", resp.pcm[0], resp.pcm[len(resp.pcm)-1])
	}
}
