package baichuanaudio

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestADTSSampleRate(t *testing.T) {
	// AAC-LC, 16 kHz sampling-frequency-index 8.
	frame := []byte{0xff, 0xf1, 0x60, 0x40, 0, 0, 0}
	// index is bits 5..2 of byte 2; explicitly force index 8.
	frame[2] = (2 << 6) | (8 << 2)
	if got := adtsSampleRate(frame); got != 16000 {
		t.Fatalf("sample rate=%d want=16000", got)
	}
	if got := adtsSampleRate([]byte{1, 2, 3}); got != 0 {
		t.Fatalf("invalid ADTS returned %d", got)
	}
}

func TestLinearResample16kTo8k(t *testing.T) {
	in := make([]int16, 1600)
	for i := range in {
		in[i] = int16(i % 1000)
	}
	out := resampleLinear(in, 16000, 8000)
	if len(out) != 800 {
		t.Fatalf("samples=%d want=800", len(out))
	}
}

func TestAACDecoderPipelineWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	gen := exec.Command(ffmpeg,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=16000:duration=0.25",
		"-ac", "1", "-c:a", "aac", "-b:a", "32k", "-f", "adts", "pipe:1")
	encoded, err := gen.Output()
	if err != nil {
		t.Fatalf("generate AAC: %v", err)
	}
	if len(encoded) < 16 || adtsSampleRate(encoded) != 16000 {
		t.Fatalf("unexpected generated ADTS stream: len=%d rate=%d", len(encoded), adtsSampleRate(encoded))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dec, err := startAACDecoder(ctx, ffmpeg, 8000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := dec.Write(encoded); err != nil {
		t.Fatal(err)
	}
	_ = dec.stdin.Close()

	var pcm bytes.Buffer
	for chunk := range dec.PCM() {
		for _, sample := range chunk {
			pcm.WriteByte(byte(sample))
			pcm.WriteByte(byte(uint16(sample) >> 8))
		}
	}
	select {
	case err := <-dec.Done():
		if err != nil {
			t.Fatalf("decoder failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("decoder did not finish")
	}
	// 0.25 s at 8 kHz is ~2000 samples. Allow AAC priming/padding variance.
	samples := pcm.Len() / 2
	if samples < 1600 || samples > 2600 {
		t.Fatalf("decoded samples=%d, expected about 2000", samples)
	}
}

func TestAACDecoderProducesPCMBeforeEOF(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	gen := exec.Command(ffmpeg,
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=1200:sample_rate=16000:duration=1.0",
		"-ac", "1", "-c:a", "aac", "-b:a", "32k", "-f", "adts", "pipe:1")
	encoded, err := gen.Output()
	if err != nil {
		t.Fatalf("generate AAC: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dec, err := startAACDecoder(ctx, ffmpeg, 8000, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	if err := dec.Write(encoded); err != nil {
		t.Fatal(err)
	}

	// The live Baichuan stream never closes between AAC frames. Verify that
	// FFmpeg emits decoded PCM without requiring EOF/flush on stdin.
	select {
	case pcm, ok := <-dec.PCM():
		if !ok || len(pcm) == 0 {
			t.Fatal("AAC decoder produced no live PCM")
		}
	case err := <-dec.Done():
		t.Fatalf("AAC decoder ended before live PCM: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("AAC decoder buffered until EOF instead of producing live PCM")
	}

	dec.Close()
	select {
	case <-dec.Done():
	case <-ctx.Done():
		t.Fatal("decoder did not terminate after cancellation")
	}
}
