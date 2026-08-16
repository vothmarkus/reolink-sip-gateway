package media

import (
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/codec"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
)

// audioControls carries only the far-end playout reference used by the WebRTC
// echo canceller. The reference is deliberately tapped at the transport write
// boundary, not when SIP RTP arrives: network jitter, inserted silence, bounded
// FIFO drops and codec framing must all be reflected in the signal/timeline the
// AEC sees as "played" toward the doorbell.
type audioControls struct {
	renderObserver func([]int16, time.Time)
}

func newAudioControls() *audioControls { return &audioControls{} }

// ObserveBaichuanPlayout reconstructs the ADPCM block that was successfully
// handed to the Reolink/Baichuan transport and converts it to the 8 kHz AEC
// reference rate. Decoding the encoded block (instead of tapping pre-ADPCM PCM)
// also includes the deterministic IMA-ADPCM quantisation seen by the device.
func (c *audioControls) ObserveBaichuanPlayout(adpcm []byte, sampleRate int, at time.Time) {
	if c == nil || c.renderObserver == nil || len(adpcm) == 0 || sampleRate < aecSampleRate {
		return
	}
	pcm := (&codec.ADPCMDecoder{}).Decode(adpcm)
	if len(pcm) == 0 {
		return
	}
	pcm = resampleBlockLinear(pcm, sampleRate, aecSampleRate)
	if len(pcm) == 0 {
		return
	}
	c.renderObserver(pcm, at)
}

// ObserveG711Playout records a successfully written 8 kHz ONVIF/RTSP
// backchannel chunk. This keeps standalone mode on the same playout-synchronised
// AEC model as Baichuan mode.
func (c *audioControls) ObserveG711Playout(payload []byte, codecName string, at time.Time) {
	if c == nil || c.renderObserver == nil || len(payload) == 0 {
		return
	}
	pcm := g711.DecodePayload(payload, codecName)
	if len(pcm) == 0 {
		return
	}
	c.renderObserver(pcm, at)
}

// SetRenderObserver installs the AEC far-end reference tap. It is configured
// before media workers start and therefore does not require a hot-path lock.
func (c *audioControls) SetRenderObserver(fn func([]int16, time.Time)) {
	if c != nil {
		c.renderObserver = fn
	}
}

func (c *audioControls) NeedsRenderReference() bool {
	return c != nil && c.renderObserver != nil
}

// resampleBlockLinear is intentionally block-local. Reolink talk profiles use
// rates that are integer multiples of 8 kHz in practice (16 kHz on the tested
// doorbell), and each Baichuan ADPCM block is independently framed. The generic
// interpolation also keeps uncommon supported rates functional without adding
// another asynchronous clock domain to the AEC reference path.
func resampleBlockLinear(in []int16, inRate, outRate int) []int16 {
	if len(in) == 0 || inRate <= 0 || outRate <= 0 {
		return nil
	}
	if inRate == outRate {
		return append([]int16(nil), in...)
	}
	outLen := int((int64(len(in))*int64(outRate) + int64(inRate)/2) / int64(inRate))
	if outLen < 1 {
		outLen = 1
	}
	out := make([]int16, outLen)
	if len(in) == 1 {
		for i := range out {
			out[i] = in[0]
		}
		return out
	}
	for i := range out {
		posNum := int64(i) * int64(inRate)
		idx := int(posNum / int64(outRate))
		frac := posNum % int64(outRate)
		if idx >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		a := int64(in[idx])
		b := int64(in[idx+1])
		v := a + (b-a)*frac/int64(outRate)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out
}
