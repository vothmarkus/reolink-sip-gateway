package g711

import (
	"bytes"
	"testing"
)

func TestConvertPayloadSameCodecCopies(t *testing.T) {
	in := []byte{0x00, 0x55, 0xaa, 0xff}
	out := ConvertPayload(in, PCMA, PCMA)
	if !bytes.Equal(out, in) {
		t.Fatalf("copy changed payload: %x -> %x", in, out)
	}
	out[0] ^= 0xff
	if in[0] != 0x00 {
		t.Fatal("ConvertPayload returned alias instead of independent copy")
	}
}

func TestConvertPayloadUsesCanonicalTables(t *testing.T) {
	input := make([]byte, 256)
	for i := range input {
		input[i] = byte(i)
	}
	gotU := ConvertPayload(input, PCMA, PCMU)
	gotA := ConvertPayload(input, PCMU, PCMA)
	for i := 0; i < 256; i++ {
		if gotU[i] != aLawToMuLaw[i] {
			t.Fatalf("A-law->mu-law byte %d = %#02x, want %#02x", i, gotU[i], aLawToMuLaw[i])
		}
		if gotA[i] != muLawToALaw[i] {
			t.Fatalf("mu-law->A-law byte %d = %#02x, want %#02x", i, gotA[i], muLawToALaw[i])
		}
	}
}

func TestConvertPayloadUnknownCodecDoesNotCorrupt(t *testing.T) {
	in := []byte{1, 2, 3, 4}
	out := ConvertPayload(in, "unknown", PCMA)
	if !bytes.Equal(out, in) {
		t.Fatalf("unknown codec changed payload: %x -> %x", in, out)
	}
}

func TestDecodePayloadKnownSilenceAndExtremes(t *testing.T) {
	mu := DecodePayload([]byte{0xff, 0x7f, 0x00, 0x80}, PCMU)
	if len(mu) != 4 || mu[0] != 0 || mu[1] != 0 || mu[2] != -32124 || mu[3] != 32124 {
		t.Fatalf("unexpected PCMU decode: %v", mu)
	}
	a := DecodePayload([]byte{0xd5, 0x55, 0x00, 0x80}, PCMA)
	if len(a) != 4 || a[0] != 8 || a[1] != -8 || a[2] != -5504 || a[3] != 5504 {
		t.Fatalf("unexpected PCMA decode: %v", a)
	}
	if got := DecodePayload([]byte{1, 2}, "unknown"); got != nil {
		t.Fatalf("unknown codec must not be guessed: %v", got)
	}
}

func TestEncodePCMKnownSilenceAndRoundTrip(t *testing.T) {
	if got := EncodePCM([]int16{0}, PCMU); len(got) != 1 || got[0] != 0xff {
		t.Fatalf("PCMU silence encoded as %x, want ff", got)
	}
	// A-law has two near-zero codewords depending on sign/quantisation. The
	// positive-zero canonical code produced here is 0xd5.
	if got := EncodePCM([]int16{0}, PCMA); len(got) != 1 || got[0] != 0xd5 {
		t.Fatalf("PCMA silence encoded as %x, want d5", got)
	}
	for _, codec := range []string{PCMA, PCMU} {
		in := []int16{-30000, -12000, -1000, 0, 1000, 12000, 30000}
		enc := EncodePCM(in, codec)
		dec := DecodePayload(enc, codec)
		if len(dec) != len(in) {
			t.Fatalf("%s roundtrip len=%d want %d", codec, len(dec), len(in))
		}
		for i := range in {
			// G.711 is lossy; bound the expected companding error instead of
			// requiring an impossible sample-perfect roundtrip.
			d := int(dec[i]) - int(in[i])
			if d < 0 {
				d = -d
			}
			if d > 1200 {
				t.Fatalf("%s sample %d roundtrip error=%d: in=%d out=%d", codec, i, d, in[i], dec[i])
			}
		}
	}
}
