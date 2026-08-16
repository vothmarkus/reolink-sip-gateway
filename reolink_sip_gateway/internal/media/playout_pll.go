package media

import (
	"math"
	"time"
)

const (
	cameraPlayoutFrameSamples    = 160  // 20 ms at 8 kHz.
	cameraPlayoutTargetSamples   = 960  // 120 ms: absorb clustered AAC decoder output without frequent edge hits.
	cameraPlayoutStartupSamples  = 960  // Prebuffer once before first live RTP frame.
	cameraPlayoutMaxSamples      = 2560 // 320 ms hard safety ceiling.
	cameraPlayoutRecoverySamples = 1440 // 180 ms retained after a genuine hard overflow.

	// Once burstiness is absorbed by queue depth, ASRC only has to track real
	// long-term clock-domain drift. Keep the correction deliberately small so it
	// cannot act as a burst controller and audibly time-warp speech.
	cameraPLLMaxCorrection = 0.20 // source samples / 20 ms = +/-1250 ppm.
	cameraPLLKp            = 1.0 / 4000.0
	cameraPLLKi            = 1.0 / 800000.0
	cameraPLLIntegralLimit = 0.10
	cameraPLLLowWatermark  = 480  // 60 ms; normal AAC sawtooth is intentionally ignored.
	cameraPLLHighWatermark = 2080 // 260 ms.

	cameraInputTimelineRebase = 750 * time.Millisecond
)

type cameraPlayoutPLLStats struct {
	Frames                 uint64
	HardDroppedSamples     uint64
	UnderrunOutputSamples  uint64
	QueueMinSamples        int
	QueueMaxSamples        int
	QueueAverageSamples    float64
	AverageCorrectionPPM   float64
	MaximumCorrectionPPM   float64
	CurrentCorrectionPPM   float64
	TargetSamples          int
	StartupSamples         int
	MaximumBufferedSamples int
	InputTimelineRebases   uint64
	InputChunks            uint64
	InputMaxChunkSamples   int
	InputMaxArrivalGap     time.Duration
}

// cameraPlayoutPLL is now a burst-tolerant bounded jitter smoother with a
// low-bandwidth ASRC/PLL. Baichuan AAC is decoded in codec/pipe-sized bursts;
// trying to hold a ~50 ms queue with rate correction caused repeated 120 ms
// edge hits in real calls. v0.4.2 instead prebuffers 120 ms, allows short bursts
// up to 320 ms, and reserves ASRC for genuine clock drift.
//
// It also carries a virtual camera-media clock. Decoder arrival bursts are
// mapped onto a continuous 8 kHz sample timeline, so AEC alignment uses when
// the camera samples belong in media time rather than when the local jitter
// buffer happened to emit them toward SIP.
type cameraPlayoutPLL struct {
	buf []int16

	phase      float64
	integral   float64
	correction float64
	active     bool

	bufStartAt  time.Time
	inputNextAt time.Time
	lastFrameAt time.Time
	lastArrival time.Time

	frames             uint64
	queueSamplesTotal  uint64
	queueObservations  uint64
	queueMin           int
	queueMax           int
	hardDropped        uint64
	underrunOutput     uint64
	correctionSum      float64
	maxAbsCorrection   float64
	haveQueueStatistic bool
	inputRebases       uint64
	inputChunks        uint64
	inputMaxChunk      int
	inputMaxArrivalGap time.Duration
}

func newCameraPlayoutPLL() *cameraPlayoutPLL {
	return &cameraPlayoutPLL{buf: make([]int16, 0, cameraPlayoutMaxSamples)}
}

// Push adds decoded 8 kHz camera PCM. arrival is only an anchor for the media
// clock; normal decoder burst timing is intentionally ignored after the first
// chunk so contiguous samples stay contiguous in AEC time.
func (p *cameraPlayoutPLL) Push(samples []int16, arrival time.Time) (dropped int, timelineRebase bool) {
	if p == nil || len(samples) == 0 {
		return 0, false
	}
	if arrival.IsZero() {
		arrival = time.Now()
	}
	p.inputChunks++
	if len(samples) > p.inputMaxChunk {
		p.inputMaxChunk = len(samples)
	}
	if !p.lastArrival.IsZero() {
		gap := arrival.Sub(p.lastArrival)
		if gap < 0 {
			gap = -gap
		}
		if gap > p.inputMaxArrivalGap {
			p.inputMaxArrivalGap = gap
		}
	}
	p.lastArrival = arrival

	chunkDur := samplesDuration(len(samples))
	nominalStart := arrival.Add(-chunkDur)
	chunkStart := nominalStart
	if !p.inputNextAt.IsZero() {
		delta := nominalStart.Sub(p.inputNextAt)
		if delta < 0 {
			delta = -delta
		}
		if delta <= cameraInputTimelineRebase {
			chunkStart = p.inputNextAt
		} else {
			// A multi-hundred-ms discontinuity is not normal AAC burst timing.
			// Rebase instead of inventing a long hidden stretch of old samples.
			p.inputRebases++
			timelineRebase = true
			if len(p.buf) > 0 {
				dropped += len(p.buf)
				p.hardDropped += uint64(len(p.buf))
				p.buf = p.buf[:0]
			}
			p.phase = 0
			p.integral = 0
			p.correction = 0
			p.bufStartAt = time.Time{}
		}
	}

	// Once live playout has emitted synthetic silence for an underrun, delayed
	// decoder data must never move the virtual capture clock backwards. Treat
	// such late data as resuming at the next media frame boundary. This keeps
	// AEC capture timestamps monotonic and prevents old audio from being played
	// later with an ever-growing conversational delay.
	if p.active && len(p.buf) == 0 && !p.lastFrameAt.IsZero() {
		playoutNext := p.lastFrameAt.Add(20 * time.Millisecond)
		if chunkStart.Before(playoutNext) {
			chunkStart = playoutNext
			timelineRebase = true
		}
	}
	p.inputNextAt = chunkStart.Add(chunkDur)
	if len(p.buf) == 0 {
		p.bufStartAt = chunkStart
	}
	p.buf = append(p.buf, samples...)

	if len(p.buf) > cameraPlayoutMaxSamples {
		// A true backlog should not become unbounded conversational latency. Drop
		// to a recovery watermark, not merely to the ceiling, to avoid repeated
		// hard-edge drops on the next decoder burst.
		drop := len(p.buf) - cameraPlayoutRecoverySamples
		if drop < 0 {
			drop = 0
		}
		if drop > 0 {
			copy(p.buf, p.buf[drop:])
			p.buf = p.buf[:len(p.buf)-drop]
			p.advanceBufferClock(float64(drop))
			p.phase = 0
			p.hardDropped += uint64(drop)
			dropped += drop
			timelineRebase = true
		}
	}
	if !p.active && len(p.buf) >= cameraPlayoutStartupSamples {
		p.active = true
	}
	p.observeQueue(len(p.buf))
	return dropped, timelineRebase
}

func (p *cameraPlayoutPLL) Len() int {
	if p == nil {
		return 0
	}
	return len(p.buf)
}

func (p *cameraPlayoutPLL) Ready() bool {
	return p != nil && p.active
}

// PopFrame returns exactly one 20 ms SIP frame plus the virtual media timestamp
// of its first source sample. ready=false is only possible during the one-time
// startup prebuffer. After playout starts, underruns produce silence to preserve
// a continuous SIP clock and are reported to the caller as discontinuities.
func (p *cameraPlayoutPLL) PopFrame(now time.Time) (out []int16, missing int, mediaAt time.Time, ready bool) {
	if p == nil || !p.active {
		return nil, 0, time.Time{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	depth := len(p.buf)
	p.observeQueue(depth)

	// Do not use the instantaneous AAC sawtooth as a rate-error signal. Only
	// nudge the ASRC when occupancy lives near a real buffer edge; inside the
	// broad 60..260 ms deadband the integral slowly relaxes toward zero.
	errorSamples := 0.0
	switch {
	case depth < cameraPLLLowWatermark:
		errorSamples = float64(depth - cameraPLLLowWatermark)
	case depth > cameraPLLHighWatermark:
		errorSamples = float64(depth - cameraPLLHighWatermark)
	default:
		p.integral *= 0.98
	}
	p.integral += errorSamples * cameraPLLKi
	if p.integral > cameraPLLIntegralLimit {
		p.integral = cameraPLLIntegralLimit
	} else if p.integral < -cameraPLLIntegralLimit {
		p.integral = -cameraPLLIntegralLimit
	}
	correction := errorSamples*cameraPLLKp + p.integral
	if correction > cameraPLLMaxCorrection {
		correction = cameraPLLMaxCorrection
	} else if correction < -cameraPLLMaxCorrection {
		correction = -cameraPLLMaxCorrection
	}
	p.correction = correction

	if len(p.buf) > 0 && !p.bufStartAt.IsZero() {
		mediaAt = p.bufStartAt.Add(samplesDurationFloat(p.phase))
	} else if !p.lastFrameAt.IsZero() {
		mediaAt = p.lastFrameAt.Add(20 * time.Millisecond)
	} else {
		mediaAt = now
	}

	ratio := (cameraPlayoutFrameSamples + correction) / cameraPlayoutFrameSamples
	out = make([]int16, cameraPlayoutFrameSamples)
	for i := range out {
		pos := p.phase + float64(i)*ratio
		idx := int(math.Floor(pos))
		frac := pos - float64(idx)
		if idx < 0 || idx >= len(p.buf) {
			missing++
			continue
		}
		a := p.buf[idx]
		b := a
		if idx+1 < len(p.buf) {
			b = p.buf[idx+1]
		}
		v := float64(a) + (float64(b)-float64(a))*frac
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out[i] = int16(math.Round(v))
	}

	endPos := p.phase + float64(cameraPlayoutFrameSamples)*ratio
	consume := int(math.Floor(endPos))
	p.phase = endPos - float64(consume)
	if consume >= len(p.buf) {
		consumed := len(p.buf)
		p.advanceBufferClock(float64(consumed))
		p.buf = p.buf[:0]
		if missing > 0 {
			p.phase = 0
		}
	} else if consume > 0 {
		copy(p.buf, p.buf[consume:])
		p.buf = p.buf[:len(p.buf)-consume]
		p.advanceBufferClock(float64(consume))
	}

	p.frames++
	p.underrunOutput += uint64(missing)
	p.correctionSum += correction
	if a := math.Abs(correction); a > p.maxAbsCorrection {
		p.maxAbsCorrection = a
	}
	p.lastFrameAt = mediaAt
	p.observeQueue(len(p.buf))
	return out, missing, mediaAt, true
}

func (p *cameraPlayoutPLL) advanceBufferClock(samples float64) {
	if p.bufStartAt.IsZero() || samples <= 0 {
		return
	}
	p.bufStartAt = p.bufStartAt.Add(samplesDurationFloat(samples))
}

func samplesDuration(n int) time.Duration {
	return time.Duration(int64(n) * int64(time.Second) / g711SampleRate)
}

func samplesDurationFloat(n float64) time.Duration {
	return time.Duration(n * float64(time.Second) / g711SampleRate)
}

func (p *cameraPlayoutPLL) observeQueue(n int) {
	if !p.haveQueueStatistic {
		p.queueMin, p.queueMax = n, n
		p.haveQueueStatistic = true
	} else {
		if n < p.queueMin {
			p.queueMin = n
		}
		if n > p.queueMax {
			p.queueMax = n
		}
	}
	p.queueSamplesTotal += uint64(n)
	p.queueObservations++
}

func (p *cameraPlayoutPLL) Stats() cameraPlayoutPLLStats {
	if p == nil {
		return cameraPlayoutPLLStats{TargetSamples: cameraPlayoutTargetSamples, StartupSamples: cameraPlayoutStartupSamples, MaximumBufferedSamples: cameraPlayoutMaxSamples}
	}
	st := cameraPlayoutPLLStats{
		Frames:                 p.frames,
		HardDroppedSamples:     p.hardDropped,
		UnderrunOutputSamples:  p.underrunOutput,
		QueueMinSamples:        p.queueMin,
		QueueMaxSamples:        p.queueMax,
		TargetSamples:          cameraPlayoutTargetSamples,
		StartupSamples:         cameraPlayoutStartupSamples,
		MaximumBufferedSamples: cameraPlayoutMaxSamples,
		CurrentCorrectionPPM:   p.correction / cameraPlayoutFrameSamples * 1_000_000,
		MaximumCorrectionPPM:   p.maxAbsCorrection / cameraPlayoutFrameSamples * 1_000_000,
		InputTimelineRebases:   p.inputRebases,
		InputChunks:            p.inputChunks,
		InputMaxChunkSamples:   p.inputMaxChunk,
		InputMaxArrivalGap:     p.inputMaxArrivalGap,
	}
	if p.queueObservations > 0 {
		st.QueueAverageSamples = float64(p.queueSamplesTotal) / float64(p.queueObservations)
	}
	if p.frames > 0 {
		st.AverageCorrectionPPM = (p.correctionSum / float64(p.frames)) / cameraPlayoutFrameSamples * 1_000_000
	}
	return st
}
