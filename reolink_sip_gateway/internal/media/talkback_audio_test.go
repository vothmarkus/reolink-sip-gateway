package media

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/codec"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
)

func TestLinearResamplerTwoTimesContinuous(t *testing.T) {
	r, err := newLinearResampler(16000)
	if err != nil {
		t.Fatal(err)
	}
	got := r.Push([]int16{0, 1000, 2000})
	want := []int16{0, 500, 1000, 1500}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d; all=%v", i, got[i], want[i], got)
		}
	}
	more := r.Push([]int16{3000})
	if len(more) != 2 || more[0] != 2000 || more[1] != 2500 {
		t.Fatalf("stream boundary interpolation failed: %v", more)
	}
}

func TestRTPSequencerReordersAndGapBecomesSilence(t *testing.T) {
	bridge, err := newPhoneAudioBuffer(g711.PCMU, 8000, 160)
	if err != nil {
		t.Fatal(err)
	}
	q := newRTPSequencer(3)
	p1 := rtp.Packet{PayloadType: 0, Sequence: 10, Timestamp: 1000, SSRC: 7, Payload: make([]byte, 160)}
	for i := range p1.Payload {
		p1.Payload[i] = 0xff
	}
	for _, f := range q.Push(p1) {
		bridge.Push(f)
	}
	p3 := p1
	p3.Sequence = 12
	p3.Timestamp = 1320
	if frames := q.Push(p3); len(frames) != 0 {
		t.Fatalf("out-of-order packet should be buffered, got %d frames", len(frames))
	}
	for _, f := range q.FlushGap() {
		bridge.Push(f)
	}
	st := bridge.Stats()
	if st.SilenceInsertedInput != 160 {
		t.Fatalf("gap inserted %d samples, want 160", st.SilenceInsertedInput)
	}
	qs := q.Stats()
	if qs.SequenceGaps != 1 || qs.PacketsReordered == 0 {
		t.Fatalf("unexpected sequencer stats: %+v", qs)
	}
	if frames := q.Push(p1); len(frames) != 0 {
		t.Fatalf("late duplicate unexpectedly emitted: %v", frames)
	}
	if q.Stats().PacketsDuplicateLate == 0 {
		t.Fatal("late duplicate was not counted")
	}
}

func TestPhoneAudioBufferBoundsLatencyOnOverflow(t *testing.T) {
	b, err := newPhoneAudioBuffer(g711.PCMU, 8000, 10)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 120)
	for i := range payload {
		payload[i] = 0xff
	}
	b.Push(sequencedPacket{packet: rtp.Packet{PayloadType: 0, Sequence: 1, Timestamp: 0, SSRC: 1, Payload: payload}, reset: true})
	if st := b.Stats(); st.FIFOOverflowSamples != 80 {
		t.Fatalf("overflow=%d, want 80", st.FIFOOverflowSamples)
	}
}

func TestElasticTalkbackStretchesSmallShortageWithoutSilence(t *testing.T) {
	const blockSamples = 1024
	fifo := newSampleFIFO(blockSamples * 4)
	fifo.Push(constantPCM(1010, 12000))
	playout := newElasticTalkbackPlayout(16000)

	result := playout.Pop(fifo, blockSamples)
	if len(result.block) != blockSamples {
		t.Fatalf("block=%d want %d", len(result.block), blockSamples)
	}
	if result.consumed != 1010 || result.validOutput != blockSamples || result.missing != 0 {
		t.Fatalf("unexpected elastic result: %+v", result)
	}
	if result.stretched != 14 || result.compressed != 0 {
		t.Fatalf("stretch/compression=%d/%d want 14/0", result.stretched, result.compressed)
	}
	if result.ratioPPM < elasticRatioScale-elasticMaxStretchPPM || result.ratioPPM > elasticRatioScale {
		t.Fatalf("ratio=%d outside allowed stretch range", result.ratioPPM)
	}
	if fifo.Len() != 0 {
		t.Fatalf("fifo retained %d samples; elastic playout introduced hidden buffering", fifo.Len())
	}
	for i, sample := range result.block {
		if sample != 12000 {
			t.Fatalf("constant signal changed at sample %d: %d", i, sample)
		}
	}
}

func TestElasticTalkbackExactBlockPassesThrough(t *testing.T) {
	const blockSamples = 160
	input := make([]int16, blockSamples)
	for i := range input {
		input[i] = int16(i*257 - 20000)
	}
	fifo := newSampleFIFO(blockSamples * 4)
	fifo.Push(input)
	playout := newElasticTalkbackPlayout(8000)

	result := playout.Pop(fifo, blockSamples)
	if result.consumed != blockSamples || result.missing != 0 || result.stretched != 0 || result.compressed != 0 || result.ratioPPM != elasticRatioScale {
		t.Fatalf("exact block was unnecessarily corrected: %+v", result)
	}
	for i := range input {
		if result.block[i] != input[i] {
			t.Fatalf("sample %d=%d want %d", i, result.block[i], input[i])
		}
	}
}

func TestElasticTalkbackCapsStretchAndFadesHardUnderrun(t *testing.T) {
	const blockSamples = 1024
	fifo := newSampleFIFO(blockSamples * 4)
	fifo.Push(constantPCM(766, 12000))
	playout := newElasticTalkbackPlayout(16000)

	result := playout.Pop(fifo, blockSamples)
	wantValid := 766 * elasticRatioScale / (elasticRatioScale - elasticMaxStretchPPM)
	if result.validOutput != wantValid || result.missing != blockSamples-wantValid {
		t.Fatalf("valid/missing=%d/%d want %d/%d", result.validOutput, result.missing, wantValid, blockSamples-wantValid)
	}
	if result.ratioPPM < elasticRatioScale-elasticMaxStretchPPM {
		t.Fatalf("hard underrun escaped 2%% stretch bound: ratio=%d", result.ratioPPM)
	}
	if !result.fadeOut {
		t.Fatal("hard underrun did not receive a fade-out")
	}
	if got := result.block[result.validOutput-1]; got != 0 {
		t.Fatalf("last valid sample=%d want 0 at silence boundary", got)
	}
	if got := result.block[result.validOutput]; got != 0 {
		t.Fatalf("first missing sample=%d want silence", got)
	}

	fifo.Push(constantPCM(blockSamples, -12000))
	recovery := playout.Pop(fifo, blockSamples)
	if !recovery.fadeIn || recovery.missing != 0 {
		t.Fatalf("recovery did not fade in cleanly: %+v", recovery)
	}
	if recovery.block[0] != 0 {
		t.Fatalf("recovery starts at %d want 0", recovery.block[0])
	}
	fadeSamples := len(playout.fadeInQ15)
	if got := recovery.block[fadeSamples-1]; got != -12000 {
		t.Fatalf("recovery did not reach full signal at sample %d: %d", fadeSamples-1, got)
	}
}

func TestElasticTalkbackShortHardUnderrunStillReachesSilence(t *testing.T) {
	// The valid signal is shorter than the nominal 5 ms / 80-sample window.
	// The window must be contracted to the available edge instead of leaving a
	// discontinuity where the padded silence begins.
	const blockSamples = 40
	fifo := newSampleFIFO(blockSamples * 4)
	fifo.Push(constantPCM(10, 12000))
	playout := newElasticTalkbackPlayout(16000)

	result := playout.Pop(fifo, blockSamples)
	if !result.fadeOut || result.validOutput >= len(playout.fadeInQ15) {
		t.Fatalf("short edge did not exercise contracted window: %+v", result)
	}
	if result.block[0] != 12000 {
		t.Fatalf("short edge did not preserve its causal start: %d", result.block[0])
	}
	if result.block[result.validOutput-1] != 0 || result.block[result.validOutput] != 0 {
		t.Fatalf("short fade did not meet silence continuously: %v", result.block)
	}
}

func TestElasticTalkbackFullUnderrunUsesCausalDecayingTail(t *testing.T) {
	const blockSamples = 1024
	fifo := newSampleFIFO(blockSamples * 4)
	fifo.Push(constantPCM(blockSamples, 5000))
	playout := newElasticTalkbackPlayout(16000)
	first := playout.Pop(fifo, blockSamples)
	if first.missing != 0 || first.block[len(first.block)-1] != 5000 {
		t.Fatalf("unexpected priming block: %+v", first)
	}

	underflow := playout.Pop(fifo, blockSamples)
	if !underflow.fadeOut || underflow.missing != blockSamples {
		t.Fatalf("full underflow did not use bounded tail: %+v", underflow)
	}
	if underflow.block[0] != 5000 {
		t.Fatalf("tail boundary jumped from 5000 to %d", underflow.block[0])
	}
	fadeSamples := len(playout.fadeInQ15)
	if underflow.block[fadeSamples-1] != 0 {
		t.Fatalf("tail did not end at zero: %d", underflow.block[fadeSamples-1])
	}
	for i, sample := range underflow.block[fadeSamples:] {
		if sample != 0 {
			t.Fatalf("post-tail sample %d=%d want silence", i+fadeSamples, sample)
		}
	}
}

func TestElasticTalkbackCompressesHighQueueWithinLimit(t *testing.T) {
	const blockSamples = 1024
	fifo := newSampleFIFO(blockSamples * 4)
	fifo.Push(constantPCM(blockSamples*4, 9000))
	playout := newElasticTalkbackPlayout(16000)

	result := playout.Pop(fifo, blockSamples)
	wantConsumed := maximumElasticInput(blockSamples)
	if result.consumed != wantConsumed || result.validOutput != blockSamples {
		t.Fatalf("consumed/output=%d/%d want %d/%d", result.consumed, result.validOutput, wantConsumed, blockSamples)
	}
	if result.compressed != wantConsumed-blockSamples || result.stretched != 0 {
		t.Fatalf("compression/stretch=%d/%d", result.compressed, result.stretched)
	}
	if result.ratioPPM > elasticRatioScale+elasticMaxCompressionPPM {
		t.Fatalf("ratio=%d escaped 3%% compression bound", result.ratioPPM)
	}
	if result.depthAfter != blockSamples*4-wantConsumed {
		t.Fatalf("post depth=%d want %d", result.depthAfter, blockSamples*4-wantConsumed)
	}
}

func TestElasticTalkbackUsesSupplyTrendAndRepaysTemporaryReserve(t *testing.T) {
	const blockSamples = 1024
	fifo := newSampleFIFO(blockSamples * 4)
	playout := newElasticTalkbackPlayout(16000)
	fifo.Push(constantPCM(blockSamples+200, 7000))
	first := playout.Pop(fifo, blockSamples)
	if first.missing != 0 {
		t.Fatalf("priming block underrun: %+v", first)
	}

	// The next interval supplies 150 samples less than nominal, but the FIFO is
	// still just above one complete block. The trend-aware controller must react
	// before a hard underrun and leave a small temporary reserve.
	fifo.Push(constantPCM(blockSamples-150, 7000))
	guarded := playout.Pop(fifo, blockSamples)
	if guarded.depthBefore < blockSamples || guarded.consumed != minimumElasticInput(blockSamples) {
		t.Fatalf("negative supply trend was not guarded: %+v", guarded)
	}
	if guarded.missing != 0 || guarded.stretched == 0 || guarded.depthAfter == 0 {
		t.Fatalf("trend guard did not preserve a bounded reserve: %+v", guarded)
	}
	reserve := guarded.depthAfter

	// Once nominal supply resumes, gentle compression must pay the reserve back
	// instead of turning the elastic correction into persistent added latency.
	for i := 0; i < 200; i++ {
		fifo.Push(constantPCM(blockSamples, 7000))
		result := playout.Pop(fifo, blockSamples)
		if result.ratioPPM < elasticRatioScale-elasticMaxStretchPPM || result.ratioPPM > elasticRatioScale+elasticMaxCompressionPPM {
			t.Fatalf("iteration %d ratio=%d outside limits", i, result.ratioPPM)
		}
	}
	if fifo.Len() >= reserve {
		t.Fatalf("temporary reserve was not repaid: after=%d initial=%d", fifo.Len(), reserve)
	}
}

func TestElasticTalkbackOverflowGetsZeroLookaheadSplice(t *testing.T) {
	const blockSamples = 1024
	fifo := newSampleFIFO(blockSamples * 4)
	playout := newElasticTalkbackPlayout(16000)
	fifo.Push(constantPCM(blockSamples, 10000))
	priming := playout.Pop(fifo, blockSamples)
	if priming.block[len(priming.block)-1] != 10000 {
		t.Fatalf("priming tail=%d want 10000", priming.block[len(priming.block)-1])
	}

	playout.MarkOverflow()
	fifo.Push(constantPCM(blockSamples, -10000))
	spliced := playout.Pop(fifo, blockSamples)
	if !spliced.overflowSplice {
		t.Fatal("overflow did not schedule a boundary splice")
	}
	if spliced.block[0] != 10000 {
		t.Fatalf("splice boundary=%d want previous output 10000", spliced.block[0])
	}
	fadeSamples := len(playout.fadeInQ15)
	if spliced.block[fadeSamples-1] != -10000 {
		t.Fatalf("splice did not reach new signal: %d", spliced.block[fadeSamples-1])
	}
	if spliced.consumed != blockSamples || spliced.depthAfter != 0 {
		t.Fatalf("splice changed timing/consumption: %+v", spliced)
	}
}

func TestElasticTalkbackConsumptionBoundsAtEveryQueueDepth(t *testing.T) {
	for _, blockSamples := range []int{40, 160, 1024} {
		for depth := 0; depth <= blockSamples*4; depth++ {
			fifo := newSampleFIFO(blockSamples * 4)
			fifo.Push(constantPCM(depth, 1000))
			playout := newElasticTalkbackPlayout(16000)

			result := playout.Pop(fifo, blockSamples)
			if len(result.block) != blockSamples {
				t.Fatalf("block=%d depth=%d output=%d", blockSamples, depth, len(result.block))
			}
			if result.consumed > depth || result.consumed > maximumElasticInput(blockSamples) {
				t.Fatalf("block=%d depth=%d over-consumed: %+v", blockSamples, depth, result)
			}
			if result.validOutput+result.missing != blockSamples {
				t.Fatalf("block=%d depth=%d invalid output accounting: %+v", blockSamples, depth, result)
			}
			if fifo.Len() != depth-result.consumed {
				t.Fatalf("block=%d depth=%d fifo=%d consumed=%d", blockSamples, depth, fifo.Len(), result.consumed)
			}
			if depth >= minimumElasticInput(blockSamples) && result.missing != 0 {
				t.Fatalf("block=%d depth=%d avoidable silence: %+v", blockSamples, depth, result)
			}
			if result.consumed > 0 && (result.ratioPPM < elasticRatioScale-elasticMaxStretchPPM || result.ratioPPM > elasticRatioScale+elasticMaxCompressionPPM) {
				t.Fatalf("block=%d depth=%d ratio=%d outside bounds", blockSamples, depth, result.ratioPPM)
			}
		}
	}
}

func TestPhoneAudioBufferReportsAvoidedAndRemainingUnderrunSeparately(t *testing.T) {
	bridge, err := newPhoneAudioBuffer(g711.PCMU, 8000, 160)
	if err != nil {
		t.Fatal(err)
	}
	bridge.PopBlock(160)
	if st := bridge.Stats(); st.FIFOPlayoutBlocks != 0 || st.FIFORawShortage != 0 || st.FIFOUnderrunSamples != 0 {
		t.Fatalf("pre-audio silence polluted playout diagnostics: %+v", st)
	}
	payload := make([]byte, 158)
	for i := range payload {
		payload[i] = 0x80
	}
	bridge.Push(sequencedPacket{packet: rtp.Packet{PayloadType: 0, Sequence: 1, Timestamp: 0, SSRC: 1, Payload: payload}, reset: true})
	block := bridge.PopBlock(160)
	if len(block) != 160 {
		t.Fatalf("block=%d want 160", len(block))
	}
	st := bridge.Stats()
	if st.FIFORawShortage != 2 || st.FIFOUnderrunSamples != 0 {
		t.Fatalf("raw/remaining shortage=%d/%d want 2/0", st.FIFORawShortage, st.FIFOUnderrunSamples)
	}
	if st.ElasticStretchBlocks != 1 || st.ElasticStretchedSamples != 2 {
		t.Fatalf("elastic stats unexpected: %+v", st)
	}
}

func TestResamplePCMBlockPreservesEndpoints(t *testing.T) {
	got := resamplePCMBlock([]int16{0, 1000}, 5)
	want := []int16{0, 250, 500, 750, 1000}
	if len(got) != len(want) {
		t.Fatalf("got %d samples want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d=%d want %d; all=%v", i, got[i], want[i], got)
		}
	}
}

func TestHalfHannWindowHasExactMonotonicEndpoints(t *testing.T) {
	window := makeHalfHannQ15(80)
	if len(window) != 80 || window[0] != 0 || window[len(window)-1] != pcmGainScale {
		t.Fatalf("unexpected half-Hann endpoints: %v ... %v", window[:2], window[len(window)-2:])
	}
	for i := 1; i < len(window); i++ {
		if window[i] < window[i-1] {
			t.Fatalf("window decreased at %d: %d < %d", i, window[i], window[i-1])
		}
	}
}

func constantPCM(samples int, value int16) []int16 {
	pcm := make([]int16, samples)
	for i := range pcm {
		pcm[i] = value
	}
	return pcm
}

type mockADPCMWriter struct {
	mu     sync.Mutex
	blocks [][]byte
	notify chan struct{}
}

func (m *mockADPCMWriter) WriteADPCMBlock(_ context.Context, block []byte) error {
	m.mu.Lock()
	m.blocks = append(m.blocks, append([]byte(nil), block...))
	m.mu.Unlock()
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return nil
}

func (m *mockADPCMWriter) first() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.blocks) == 0 {
		return nil
	}
	return append([]byte(nil), m.blocks[0]...)
}

func TestBaichuanAudioBridgeMockSIPRTPToADPCM(t *testing.T) {
	gateway, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	phone, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()

	call := &sip.Call{
		Codec:     sip.Codec{Name: g711.PCMU, PayloadType: 0},
		RemoteRTP: phone.LocalAddr().(*net.UDPAddr),
	}
	writer := &mockADPCMWriter{notify: make(chan struct{}, 8)}
	peerDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runBaichuanAudioBridge(ctx, gateway, call, writer, 16000, 320, nil, nil, nil, peerDone, nil, 0)
	}()

	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = 0x80 // high positive PCMU amplitude
	}
	pkt := rtp.Marshal(rtp.Packet{PayloadType: 0, Sequence: 1, Timestamp: 0, SSRC: 99, Payload: payload})
	if _, err := phone.WriteToUDP(pkt, gateway.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writer.notify:
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("timed out waiting for paced ADPCM block")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("bridge shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}

	block := writer.first()
	if len(block) != 164 { // 320 samples -> 4-byte IMA header + 160 encoded bytes
		t.Fatalf("ADPCM block len=%d, want 164", len(block))
	}
	decoded := (&codec.ADPCMDecoder{}).Decode(block)
	var peak int16
	for _, sample := range decoded {
		if sample > peak {
			peak = sample
		}
	}
	if peak < 1000 {
		t.Fatalf("mock RTP signal was not carried into ADPCM, peak=%d", peak)
	}
}

func TestReceivePhoneRTPRetargetsOnlyValidatedMedia(t *testing.T) {
	gateway, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	original, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	alternate, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer alternate.Close()

	call := &sip.Call{Codec: sip.Codec{Name: g711.PCMU, PayloadType: 0}, RemoteRTP: original.LocalAddr().(*net.UDPAddr)}
	bridge, err := newPhoneAudioBuffer(g711.PCMU, 8000, 160)
	if err != nil {
		t.Fatal(err)
	}
	sequencer := newRTPSequencer(defaultReorderWindow)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- receivePhoneRTP(ctx, gateway, call, bridge, sequencer, nil, 0) }()

	// Malformed traffic from the correct PBX IP but a different source port must
	// not be allowed to retarget the symmetric RTP destination.
	if _, err := alternate.WriteToUDP([]byte("not rtp"), gateway.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := call.RemoteRTPAddr().Port; got != original.LocalAddr().(*net.UDPAddr).Port {
		t.Fatalf("malformed UDP retargeted RTP port to %d", got)
	}

	// A valid packet with the negotiated payload type may retarget it.
	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = 0xff
	}
	packet := rtp.Marshal(rtp.Packet{PayloadType: 0, Sequence: 1, Timestamp: 0, SSRC: 3, Payload: payload})
	if _, err := alternate.WriteToUDP(packet, gateway.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for call.RemoteRTPAddr().Port != alternate.LocalAddr().(*net.UDPAddr).Port && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := call.RemoteRTPAddr().Port; got != alternate.LocalAddr().(*net.UDPAddr).Port {
		t.Fatalf("valid RTP did not retarget port: got %d want %d", got, alternate.LocalAddr().(*net.UDPAddr).Port)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("receiver shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("receiver did not stop")
	}
}

func TestReceivePhoneRTPAcceptsDTMFOnlyFromNegotiatedPortWithoutRetargeting(t *testing.T) {
	gateway, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	phone, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	alternate, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer alternate.Close()

	call := &sip.Call{
		Codec:          sip.Codec{Name: g711.PCMU, PayloadType: 0},
		TelephoneEvent: &sip.TelephoneEvent{PayloadType: 101, ClockRate: 8000},
		RemoteRTP:      phone.LocalAddr().(*net.UDPAddr),
	}
	bridge, err := newPhoneAudioBuffer(g711.PCMU, 8000, 160)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan rtp.Packet, 2)
	handle := func(packet rtp.Packet) bool {
		if packet.PayloadType != call.TelephoneEvent.PayloadType {
			return false
		}
		received <- packet
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- receivePhoneRTP(ctx, gateway, call, bridge, newRTPSequencer(defaultReorderWindow), handle, 0)
	}()

	packet := rtp.Marshal(rtp.Packet{PayloadType: 101, Sequence: 1, Timestamp: 1000, SSRC: 4, Payload: []byte{5, 0x80, 0x03, 0x20}})
	if _, err := alternate.WriteToUDP(packet, gateway.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
		t.Fatal("DTMF from an unnegotiated source port was accepted")
	case <-time.After(40 * time.Millisecond):
	}
	if got := call.RemoteRTPAddr().Port; got != phone.LocalAddr().(*net.UDPAddr).Port {
		t.Fatalf("DTMF retargeted RTP port to %d", got)
	}

	if _, err := phone.WriteToUDP(packet, gateway.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got.PayloadType != 101 {
			t.Fatalf("unexpected telephone-event packet: %#v", got)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("DTMF from the negotiated source port was not accepted")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("receiver shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("receiver did not stop")
	}
}

func TestReceivePhoneRTPWatchdogEndsAbandonedCall(t *testing.T) {
	gateway, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	phone, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()

	call := &sip.Call{Codec: sip.Codec{Name: g711.PCMA, PayloadType: 8}, RemoteRTP: phone.LocalAddr().(*net.UDPAddr)}
	bridge, err := newPhoneAudioBuffer(g711.PCMA, 8000, 160)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = receivePhoneRTP(context.Background(), gateway, call, bridge, newRTPSequencer(defaultReorderWindow), nil, 60*time.Millisecond)
	if !errors.Is(err, ErrRTPInactivity) {
		t.Fatalf("watchdog result=%v", err)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond || elapsed > 400*time.Millisecond {
		t.Fatalf("watchdog elapsed=%s", elapsed)
	}
}

func TestBaichuanAudioBridgeProducesSilenceDuringVADUnderrun(t *testing.T) {
	gateway, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	phone, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()

	call := &sip.Call{Codec: sip.Codec{Name: g711.PCMA, PayloadType: 8}, RemoteRTP: phone.LocalAddr().(*net.UDPAddr)}
	writer := &mockADPCMWriter{notify: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runBaichuanAudioBridge(ctx, gateway, call, writer, 16000, 320, nil, nil, nil, make(chan struct{}), nil, 0)
	}()

	select {
	case <-writer.notify:
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("timed out waiting for paced silence block")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("bridge shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}
	decoded := (&codec.ADPCMDecoder{}).Decode(writer.first())
	for i, sample := range decoded {
		if sample != 0 {
			t.Fatalf("silence block sample %d = %d, want 0", i, sample)
		}
	}
}

func TestBaichuanAudioBridgeAECReferenceIsTappedAtEncodedPlayout(t *testing.T) {
	gateway, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	phone, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()

	call := &sip.Call{Codec: sip.Codec{Name: g711.PCMU, PayloadType: 0}, RemoteRTP: phone.LocalAddr().(*net.UDPAddr)}
	writer := &mockADPCMWriter{notify: make(chan struct{}, 8)}
	controls := newAudioControls()
	type observation struct {
		pcm []int16
		at  time.Time
	}
	observed := make(chan observation, 8)
	controls.SetRenderObserver(func(pcm []int16, at time.Time) {
		cp := append([]int16(nil), pcm...)
		select {
		case observed <- observation{pcm: cp, at: at}:
		default:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runBaichuanAudioBridge(ctx, gateway, call, writer, 16000, 320, controls, nil, nil, make(chan struct{}), nil, 0)
	}()

	payload := make([]byte, 160)
	for i := range payload {
		payload[i] = 0x80
	}
	pkt := rtp.Marshal(rtp.Packet{PayloadType: 0, Sequence: 1, Timestamp: 0, SSRC: 77, Payload: payload})
	sipArrival := time.Now()
	if _, err := phone.WriteToUDP(pkt, gateway.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(600 * time.Millisecond)
	var got observation
	for {
		select {
		case got = <-observed:
			peak := int16(0)
			for _, v := range got.pcm {
				if v < 0 {
					v = -v
				}
				if v > peak {
					peak = v
				}
			}
			if peak > 1000 {
				goto found
			}
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for non-silent playout-synchronised AEC reference")
		}
	}
found:
	if len(got.pcm) != 160 { // 20 ms at 16 kHz -> 20 ms / 160 samples at 8 kHz.
		t.Fatalf("reference samples=%d want 160", len(got.pcm))
	}
	if got.at.IsZero() {
		t.Fatal("reference playout timestamp is zero")
	}
	// A SIP-arrival tap would observe immediately. The 20 ms Baichuan block
	// cadence must elapse first, proving the reference is anchored at encoded
	// transport playout instead of at the incoming RTP packet.
	if got.at.Sub(sipArrival) < 10*time.Millisecond {
		t.Fatalf("reference timestamp %s is too close to SIP arrival %s; playout tap not proven", got.at, sipArrival)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("bridge shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}
}
