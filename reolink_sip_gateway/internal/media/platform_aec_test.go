package media

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/platformaudio"
)

type fakePlatformAEC struct {
	highPass, noise bool
	requests        [][]byte
	stopped         int
	failure         error
}

func (f *fakePlatformAEC) StartAACDecoder(int32, int32) (int64, error) { return 0, nil }
func (f *fakePlatformAEC) DecodeAAC(int64, []byte) ([]byte, error)     { return nil, nil }
func (f *fakePlatformAEC) StopAACDecoder(int64)                        {}
func (f *fakePlatformAEC) StartAEC(h, n bool) (int64, error) {
	f.highPass, f.noise = h, n
	return 17, nil
}
func (f *fakePlatformAEC) StopAEC(h int64) {
	if h == 17 {
		f.stopped++
	}
}
func (f *fakePlatformAEC) ProcessAEC(h int64, request []byte) ([]byte, error) {
	if h != 17 {
		return nil, errors.New("wrong handle")
	}
	f.requests = append(f.requests, append([]byte(nil), request...))
	if f.failure != nil {
		return nil, f.failure
	}
	pcm := make([]int16, aecFrameSamples)
	pcm[0] = -2345
	return encodeNativeAECResponse(0, nativeAECStats{ValidMask: nativeStatERLE, EchoReturnLossEnhancementDB: 12.5}, pcm), nil
}

func TestAndroidAECUsesAdapterAndSharedFrameProtocol(t *testing.T) {
	fake := &fakePlatformAEC{}
	restore := platformaudio.Set(fake)
	defer restore()
	cfg := config.Defaults()
	cfg.WebRTCHighPassFilterEnabled = true
	cfg.WebRTCNoiseSuppressionEnabled = false
	ec, err := startEchoCanceller(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ec.Close()
	if !fake.highPass || fake.noise || len(fake.requests) != 6 {
		t.Fatalf("wrong options/warmup: %+v", fake)
	}
	render, capture := make([]int16, 80), make([]int16, 80)
	render[0], capture[0] = -1234, 5678
	pcm, err := ec.processor.Process(context.Background(), render, capture)
	if err != nil || pcm[0] != -2345 {
		t.Fatalf("output: %v %v", pcm, err)
	}
	wire := fake.requests[len(fake.requests)-1]
	if len(wire) != 324 || string(wire[:4]) != "AEC1" || int16(binary.LittleEndian.Uint16(wire[4:])) != -1234 || int16(binary.LittleEndian.Uint16(wire[164:])) != 5678 {
		t.Fatal("render/capture order or PCM endianness changed")
	}
	stats := ec.processor.(echoProcessorStatsProvider).NativeStats()
	if !stats.has(nativeStatERLE) || stats.EchoReturnLossEnhancementDB != 12.5 {
		t.Fatalf("statistics lost: %+v", stats)
	}
	ec.Close()
	ec.Close()
	if fake.stopped != 1 {
		t.Fatalf("closed %d times", fake.stopped)
	}
	if _, err := ec.processor.Process(context.Background(), render, capture); err == nil {
		t.Fatal("closed processor accepted a frame")
	}
}

func TestAndroidAECFailureAndCancellationDoNotBypassProcessing(t *testing.T) {
	fake := &fakePlatformAEC{failure: errors.New("DSP failed")}
	proc, err := newPlatformAECProcessor(fake, nativeAECOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	frame := make([]int16, 80)
	if _, err = proc.Process(context.Background(), frame, frame); err == nil {
		t.Fatal("DSP failure ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(fake.requests)
	if _, err = proc.Process(ctx, frame, frame); !errors.Is(err, context.Canceled) || len(fake.requests) != before {
		t.Fatal("cancelled frame processed")
	}
	if _, err = proc.Process(context.Background(), frame[:79], frame); err == nil {
		t.Fatal("partial frame accepted")
	}
}
