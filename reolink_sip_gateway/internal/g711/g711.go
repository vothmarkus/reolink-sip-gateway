package g711

import (
	"math"
	"strings"
)

const (
	PCMA = "pcma"
	PCMU = "pcmu"
)

// ConvertPayload transcodes G.711 A-law and mu-law byte-for-byte. Both codecs
// represent one 8 kHz sample per byte, so transcoding does not change payload
// length or RTP timestamp progression.
func ConvertPayload(payload []byte, from, to string) []byte {
	from = strings.ToLower(from)
	to = strings.ToLower(to)
	out := make([]byte, len(payload))
	if from == to {
		copy(out, payload)
		return out
	}
	var table *[256]byte
	switch {
	case from == PCMA && to == PCMU:
		table = &aLawToMuLaw
	case from == PCMU && to == PCMA:
		table = &muLawToALaw
	default:
		// Callers negotiate only PCMA/PCMU; preserving the payload is safer than
		// silently interpreting an unknown codec as one of them.
		copy(out, payload)
		return out
	}
	for i, b := range payload {
		out[i] = table[b]
	}
	return out
}

// DecodePayload converts one G.711 payload into signed 16-bit linear PCM.
// G.711 RTP uses one 8 kHz sample per payload byte.
func DecodePayload(payload []byte, codec string) []int16 {
	out := make([]int16, len(payload))
	switch strings.ToLower(codec) {
	case PCMA:
		for i, b := range payload {
			out[i] = decodeALaw(b)
		}
	case PCMU:
		for i, b := range payload {
			out[i] = decodeMuLaw(b)
		}
	default:
		return nil
	}
	return out
}

// EncodePCM converts signed 16-bit linear PCM to G.711. It is used by media
// paths that operate in PCM, including echo-cancelled camera audio.
func EncodePCM(pcm []int16, codec string) []byte {
	out := make([]byte, len(pcm))
	switch strings.ToLower(codec) {
	case PCMA:
		for i, sample := range pcm {
			out[i] = encodeALaw(sample)
		}
	case PCMU:
		for i, sample := range pcm {
			out[i] = encodeMuLaw(sample)
		}
	default:
		return nil
	}
	return out
}

// RMSDBFS returns the RMS level in dBFS. Silence is represented as -120 dBFS
// to keep comparisons finite and deterministic.
func RMSDBFS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return -120
	}
	var sum float64
	for _, s := range pcm {
		v := float64(s)
		sum += v * v
	}
	if sum == 0 {
		return -120
	}
	rms := math.Sqrt(sum / float64(len(pcm)))
	return 20 * math.Log10(rms/32768.0)
}

// decodeMuLaw implements ITU-T G.711 mu-law expansion.
func decodeMuLaw(v byte) int16 {
	v = ^v
	t := ((int(v) & 0x0f) << 3) + 0x84
	t <<= (uint(v) & 0x70) >> 4
	if v&0x80 != 0 {
		return int16(0x84 - t)
	}
	return int16(t - 0x84)
}

// decodeALaw implements ITU-T G.711 A-law expansion.
func decodeALaw(v byte) int16 {
	v ^= 0x55
	t := (int(v&0x0f) << 4) + 8
	seg := int((v & 0x70) >> 4)
	if seg >= 1 {
		t += 0x100
	}
	if seg > 1 {
		t <<= uint(seg - 1)
	}
	if v&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

// encodeMuLaw implements the canonical 16-bit linear PCM -> G.711 mu-law
// companding rule. It intentionally has no optional zero trap.
func encodeMuLaw(sample int16) byte {
	pcm := int(sample)
	sign := 0
	if pcm < 0 {
		sign = 0x80
		pcm = -pcm
		if pcm > 32767 {
			pcm = 32767
		}
	}
	const (
		bias = 0x84
		clip = 32635
	)
	if pcm > clip {
		pcm = clip
	}
	pcm += bias

	exponent := 7
	mask := 0x4000
	for exponent > 0 && pcm&mask == 0 {
		exponent--
		mask >>= 1
	}
	mantissa := (pcm >> uint(exponent+3)) & 0x0f
	return ^byte(sign | exponent<<4 | mantissa)
}

// encodeALaw implements the canonical 16-bit linear PCM -> G.711 A-law
// companding rule used for RTP PCMA.
func encodeALaw(sample int16) byte {
	pcm := int(sample)
	mask := 0xd5
	if pcm < 0 {
		mask = 0x55
		pcm = -pcm - 1
	}
	if pcm > 32767 {
		pcm = 32767
	}

	var aval int
	if pcm < 256 {
		aval = pcm >> 4
	} else {
		exponent := 1
		for threshold := 0x200; exponent < 7 && pcm >= threshold; threshold <<= 1 {
			exponent++
		}
		aval = exponent<<4 | ((pcm >> uint(exponent+3)) & 0x0f)
	}
	return byte(aval ^ mask)
}
