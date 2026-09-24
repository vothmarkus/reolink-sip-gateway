package baichuanaudio

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/audiostats"
	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
	"github.com/vothmarkus/reolink-sip-gateway/internal/platformaudio"
)

type fakePlatformAAC struct {
	decode  func() ([]byte, error)
	stopped int
}

func (f *fakePlatformAAC) StartAACDecoder(rate, channels int32) (int64, error)  { return 42, nil }
func (f *fakePlatformAAC) DecodeAAC(handle int64, frame []byte) ([]byte, error) { return f.decode() }
func (f *fakePlatformAAC) StopAACDecoder(handle int64)                          { f.stopped++ }

func TestPlatformAACRepeatedDecodeAndResampling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	adapter := &fakePlatformAAC{decode: func() ([]byte, error) {
		// Little-endian PCM: -32768, 0, 16384, 0 at 16 kHz.
		return []byte{0, 0x80, 0, 0, 0, 0x40, 0, 0}, nil
	}}
	dec, err := startPlatformAACDecoder(ctx, adapter, 16000, 1, 8000)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	// More than the former eight-element self-owned queue: no second
	// goroutine should be needed to drain a synchronous platform decoder.
	for i := 0; i < 100; i++ {
		pcm, err := dec.Decode([]byte{1})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pcm, []int16{-32768, 16384}) {
			t.Fatalf("PCM=%v", pcm)
		}
	}
	adapter.decode = func() ([]byte, error) { return []byte{1}, nil }
	if _, err := dec.Decode(nil); err == nil {
		t.Fatal("odd PCM byte count accepted")
	}
	cancel()
	if _, err := dec.Decode(nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	dec.Close()
	dec.Close()
	if adapter.stopped != 1 {
		t.Fatalf("stop count=%d", adapter.stopped)
	}
}

func TestPlatformReceiveWaitsForDecodedAudio(t *testing.T) {
	firstDecoded := make(chan struct{})
	calls := 0
	adapter := &fakePlatformAAC{decode: func() ([]byte, error) {
		calls++
		if calls == 1 {
			close(firstDecoded)
			return nil, nil
		}
		return []byte{0, 0x80, 0, 0, 0, 0x40, 0, 0}, nil
	}}
	defer platformaudio.Set(adapter)()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r := &Receiver{pcm: make(chan []int16, 8), ready: make(chan Info, 1), done: make(chan error, 1)}
	packets := make(chan baichuan.MediaPacket, 128)
	var stats audiostats.Counters
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		r.receive(ctx, Config{OutputRate: 8000, Stats: &stats}, 16000, packets, nil, func() error { return nil })
	}()
	defer func() { cancel(); <-finished }()
	packet := baichuan.MediaPacket{Kind: baichuan.MediaPacketAAC, Data: []byte{0xff, 0xf1, 0x60, 0x40, 1, 0x5f, 0xfc, 1, 2, 3}}
	packets <- packet
	select {
	case <-firstDecoded:
	case <-ctx.Done():
		t.Fatal("decoder not called")
	}
	select {
	case <-r.Ready():
		t.Fatal("reported ready without PCM")
	default:
	}
	for i := 0; i < 100; i++ {
		packets <- packet
	}
	select {
	case info := <-r.Ready():
		if info.InputSampleRate != 16000 || info.Codec != "aac" {
			t.Fatalf("info=%+v", info)
		}
	case <-ctx.Done():
		t.Fatal("no audio readiness")
	}
	for i := 0; i < 100; i++ {
		select {
		case pcm := <-r.PCM():
			if !reflect.DeepEqual(pcm, []int16{-32768, 16384}) {
				t.Fatalf("PCM=%v", pcm)
			}
		case <-ctx.Done():
			t.Fatal("receive loop stalled")
		}
	}
	cancel()
	<-finished
	if adapter.stopped != 1 {
		t.Fatalf("decoder cleanup count=%d", adapter.stopped)
	}
	snapshot := stats.Snapshot()
	if snapshot.Packets != 101 || snapshot.PCMSamples != 200 || snapshot.PCMPeak != 32768 {
		t.Fatalf("diagnostics=%+v", snapshot)
	}
}

func TestPlatformDecoderFailureDoesNotReportReady(t *testing.T) {
	adapter := &fakePlatformAAC{decode: func() ([]byte, error) { return nil, errors.New("codec unavailable") }}
	defer platformaudio.Set(adapter)()
	r := &Receiver{pcm: make(chan []int16, 1), ready: make(chan Info, 1), done: make(chan error, 1)}
	packets := make(chan baichuan.MediaPacket, 1)
	packets <- baichuan.MediaPacket{Kind: baichuan.MediaPacketAAC, Data: []byte{0xff, 0xf1, 0x60, 0x40}}
	r.receive(context.Background(), Config{OutputRate: 8000}, 16000, packets, nil, func() error { return nil })
	if _, ok := <-r.Ready(); ok {
		t.Fatal("reported ready after decoder failure")
	}
	if err := <-r.Done(); err == nil || !strings.Contains(err.Error(), "codec unavailable") {
		t.Fatalf("error=%v", err)
	}
	if adapter.stopped != 1 {
		t.Fatalf("decoder cleanup count=%d", adapter.stopped)
	}
}

func TestPlatformCalibrationCaptureKeepsSixteenKHzPCMWithoutFFmpeg(t *testing.T) {
	adapter := &fakePlatformAAC{decode: func() ([]byte, error) { return []byte{0, 0x80, 0, 0, 0, 0x40, 0, 0}, nil }}
	defer platformaudio.Set(adapter)()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &Receiver{pcm: make(chan []int16, 1), ready: make(chan Info, 1), done: make(chan error, 1)}
	packets := make(chan baichuan.MediaPacket, 1)
	packets <- baichuan.MediaPacket{Kind: baichuan.MediaPacketAAC, Data: []byte{0xff, 0xf1, 0x60, 0x40}}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		r.receive(ctx, Config{OutputRate: 16000, FFmpegPath: "/no-ffmpeg-on-android"}, 16000, packets, nil, func() error { return nil })
	}()
	defer func() { cancel(); <-finished }()
	select {
	case pcm := <-r.PCM():
		if !reflect.DeepEqual(pcm, []int16{-32768, 0, 16384, 0}) {
			t.Fatalf("calibration PCM altered: %v", pcm)
		}
		info := <-r.Ready()
		if info.OutputSampleRate != 16000 || info.InputSampleRate != 16000 {
			t.Fatalf("wrong calibration sample clock: %+v", info)
		}
	case <-time.After(time.Second):
		t.Fatal("calibration decoder did not produce PCM")
	}
}
