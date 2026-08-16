package media

import (
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/codec"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
)

func TestAudioControlsBaichuanObserverSeesEncodedPlayoutAtAECrate(t *testing.T) {
	c := newAudioControls()
	var observed []int16
	var observedAt time.Time
	c.SetRenderObserver(func(pcm []int16, at time.Time) {
		observed = append([]int16(nil), pcm...)
		observedAt = at
	})

	pcm16 := make([]int16, 320) // 20 ms at 16 kHz -> 160 samples at AEC 8 kHz.
	for i := range pcm16 {
		pcm16[i] = int16((i%80 - 40) * 500)
	}
	adpcm, err := (&codec.ADPCMEncoder{}).EncodeBlock(pcm16)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1000, 123000000)
	c.ObserveBaichuanPlayout(adpcm, 16000, at)
	if len(observed) != 160 {
		t.Fatalf("observer samples=%d want 160", len(observed))
	}
	if !observedAt.Equal(at) {
		t.Fatalf("observer time=%s want %s", observedAt, at)
	}
	peak := int16(0)
	for _, s := range observed {
		if s < 0 {
			s = -s
		}
		if s > peak {
			peak = s
		}
	}
	if peak < 1000 {
		t.Fatalf("ADPCM playout reference unexpectedly silent, peak=%d", peak)
	}
	if !c.NeedsRenderReference() {
		t.Fatal("AEC observer must report active render reference")
	}
}

func TestAudioControlsG711ObserverUsesWrittenCodec(t *testing.T) {
	c := newAudioControls()
	var observed []int16
	c.SetRenderObserver(func(pcm []int16, _ time.Time) { observed = append([]int16(nil), pcm...) })
	in := make([]int16, 160)
	for i := range in {
		in[i] = int16((i - 80) * 120)
	}
	payload := g711.EncodePCM(in, g711.PCMA)
	c.ObserveG711Playout(payload, g711.PCMA, time.Now())
	if len(observed) != 160 {
		t.Fatalf("observer samples=%d want 160", len(observed))
	}
	if g711.RMSDBFS(observed) < -50 {
		t.Fatal("G.711 playout reference unexpectedly silent")
	}
}

func TestAudioControlsWithoutObserverDoesNothing(t *testing.T) {
	c := newAudioControls()
	if c.NeedsRenderReference() {
		t.Fatal("reference tap unexpectedly active")
	}
	// No observer: both calls must be harmless.
	c.ObserveG711Playout([]byte{0xd5, 0xd5}, g711.PCMA, time.Now())
	c.ObserveBaichuanPlayout([]byte{0, 0, 0, 0}, 16000, time.Now())
}
