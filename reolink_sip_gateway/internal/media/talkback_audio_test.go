package media

import (
	"context"
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
		done <- runBaichuanAudioBridge(ctx, gateway, call, writer, 16000, 320, nil, nil, peerDone, nil)
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
	go func() { done <- receivePhoneRTP(ctx, gateway, call, bridge, sequencer) }()

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
		done <- runBaichuanAudioBridge(ctx, gateway, call, writer, 16000, 320, nil, nil, make(chan struct{}), nil)
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
		done <- runBaichuanAudioBridge(ctx, gateway, call, writer, 16000, 320, controls, nil, make(chan struct{}), nil)
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
