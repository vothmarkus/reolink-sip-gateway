package media

import (
	"math"
	"time"
)

const (
	elasticRatioScale        = 1_000_000
	elasticMaxStretchPPM     = 20_000 // At most 2% time expansion; no extra prebuffer is introduced.
	elasticMaxCompressionPPM = 30_000 // At most 3% time compression to drain a growing backlog.
	elasticTrendAlpha        = 0.25
	elasticFadeDuration      = 5 * time.Millisecond
	pcmGainScale             = 32_768
)

type elasticPopResult struct {
	block          []int16
	depthBefore    int
	depthAfter     int
	consumed       int
	validOutput    int
	rawShortage    int
	missing        int
	ratioPPM       int
	stretched      int
	compressed     int
	fadeOut        bool
	fadeIn         bool
	overflowSplice bool
	supplyTrend    float64
}

// elasticTalkbackPlayout turns the bounded FIFO into an elastic real-time
// source. It never waits for more input, changes the Baichuan block cadence or
// enlarges the FIFO. Instead it consumes up to 2% fewer or 3% more PCM samples
// for the block that is already due and linearly maps them onto the exact
// negotiated block size. Hard shortages remain possible and are softened with
// a causal 5 ms half-Hann edge; hard FIFO drops receive the same kind of
// zero-lookahead splice on the next block.
type elasticTalkbackPlayout struct {
	fadeInQ15 []int32

	havePostDepth bool
	lastPostDepth int
	haveTrend     bool
	supplyTrend   float64

	recoveryPending bool
	overflowPending bool
	haveLastOutput  bool
	lastOutput      int16
}

func newElasticTalkbackPlayout(outputRate int) *elasticTalkbackPlayout {
	fadeSamples := int((int64(outputRate)*int64(elasticFadeDuration) + int64(time.Second) - 1) / int64(time.Second))
	if fadeSamples < 2 {
		fadeSamples = 2
	}
	return &elasticTalkbackPlayout{fadeInQ15: makeHalfHannQ15(fadeSamples)}
}

func (p *elasticTalkbackPlayout) ResetTimeline() {
	if p == nil {
		return
	}
	p.havePostDepth = false
	p.lastPostDepth = 0
	p.haveTrend = false
	p.supplyTrend = 0
	p.overflowPending = true
}

func (p *elasticTalkbackPlayout) MarkOverflow() {
	if p == nil {
		return
	}
	p.havePostDepth = false
	p.haveTrend = false
	p.supplyTrend = 0
	p.overflowPending = true
}

func (p *elasticTalkbackPlayout) Pop(fifo *sampleFIFO, blockSamples int) elasticPopResult {
	result := elasticPopResult{block: make([]int16, maxInt(blockSamples, 0)), ratioPPM: elasticRatioScale}
	if p == nil || fifo == nil || blockSamples <= 0 {
		return result
	}
	result.depthBefore = fifo.Len()
	result.rawShortage = maxInt(0, blockSamples-result.depthBefore)

	consume := p.chooseConsume(result.depthBefore, blockSamples)
	if consume > result.depthBefore {
		consume = result.depthBefore
	}
	input := fifo.Pop(consume)
	result.consumed = len(input)
	result.validOutput = blockSamples
	minimumInput := minimumElasticInput(blockSamples)
	if result.consumed < minimumInput {
		result.validOutput = result.consumed * elasticRatioScale / (elasticRatioScale - elasticMaxStretchPPM)
		if result.validOutput < result.consumed {
			result.validOutput = result.consumed
		}
		if result.validOutput > blockSamples {
			result.validOutput = blockSamples
		}
	}
	if result.consumed == 0 {
		result.validOutput = 0
	}
	if result.validOutput > 0 {
		copy(result.block, resamplePCMBlock(input, result.validOutput))
		result.ratioPPM = result.consumed * elasticRatioScale / result.validOutput
	}
	result.stretched = maxInt(0, result.validOutput-result.consumed)
	result.compressed = maxInt(0, result.consumed-result.validOutput)
	result.missing = blockSamples - result.validOutput

	switch {
	case result.validOutput > 0 && p.recoveryPending:
		applyFadeIn(result.block[:result.validOutput], p.fadeInQ15)
		result.fadeIn = true
		p.recoveryPending = false
		p.overflowPending = false
	case result.validOutput > 0 && p.overflowPending && p.haveLastOutput:
		applyBoundarySplice(result.block[:result.validOutput], p.lastOutput, p.fadeInQ15)
		result.overflowSplice = true
		p.overflowPending = false
	case result.validOutput > 0:
		p.overflowPending = false
	}

	if result.missing > 0 {
		if result.validOutput > 0 {
			applyFadeOut(result.block[:result.validOutput], p.fadeInQ15)
			result.fadeOut = true
		} else if p.haveLastOutput && p.lastOutput != 0 {
			applyDecayingTail(result.block, p.lastOutput, p.fadeInQ15)
			result.fadeOut = true
		}
		p.recoveryPending = true
		p.overflowPending = false
		p.havePostDepth = false
		p.haveTrend = false
		p.supplyTrend = 0
	}

	if len(result.block) > 0 {
		p.lastOutput = result.block[len(result.block)-1]
		p.haveLastOutput = true
	}
	result.depthAfter = fifo.Len()
	if result.missing == 0 {
		p.lastPostDepth = result.depthAfter
		p.havePostDepth = true
	}
	result.supplyTrend = p.supplyTrend
	return result
}

func (p *elasticTalkbackPlayout) chooseConsume(depth, blockSamples int) int {
	if depth <= 0 || blockSamples <= 0 {
		return 0
	}
	if p.havePostDepth {
		supplied := depth - p.lastPostDepth
		drift := float64(supplied - blockSamples)
		if p.haveTrend {
			p.supplyTrend = (1-elasticTrendAlpha)*p.supplyTrend + elasticTrendAlpha*drift
		} else {
			p.supplyTrend = drift
			p.haveTrend = true
		}
	}

	minimum := minimumElasticInput(blockSamples)
	maximum := maximumElasticInput(blockSamples)
	consume := blockSamples
	projected := float64(depth)
	if p.haveTrend {
		projected += p.supplyTrend
	}

	if depth < blockSamples {
		consume = depth
	} else if projected < float64(blockSamples) {
		reserve := int(math.Ceil(float64(blockSamples) - projected))
		consume = blockSamples - minInt(blockSamples-minimum, reserve)
	} else {
		pressure := math.Max(float64(depth), projected)
		// Any reserve left by a previous stretch is paid back very gently once
		// the FIFO again holds more than the block that is due. This prevents the
		// elastic controller itself from creating a persistent latency offset.
		highWater := float64(blockSamples)
		ceiling := float64(blockSamples * 4)
		if pressure > highWater {
			fraction := (pressure - highWater) / (ceiling - highWater)
			if fraction > 1 {
				fraction = 1
			}
			consume = blockSamples + int(math.Round(fraction*float64(maximum-blockSamples)))
		}
	}
	if consume < minimum && depth >= minimum {
		consume = minimum
	}
	if consume > maximum {
		consume = maximum
	}
	if consume > depth {
		consume = depth
	}
	return consume
}

func minimumElasticInput(blockSamples int) int {
	return (blockSamples*(elasticRatioScale-elasticMaxStretchPPM) + elasticRatioScale - 1) / elasticRatioScale
}

func maximumElasticInput(blockSamples int) int {
	return blockSamples * (elasticRatioScale + elasticMaxCompressionPPM) / elasticRatioScale
}

func resamplePCMBlock(input []int16, outputSamples int) []int16 {
	if outputSamples <= 0 {
		return nil
	}
	output := make([]int16, outputSamples)
	if len(input) == 0 {
		return output
	}
	if len(input) == 1 {
		for i := range output {
			output[i] = input[0]
		}
		return output
	}
	if len(input) == outputSamples {
		copy(output, input)
		return output
	}
	if outputSamples == 1 {
		output[0] = input[0]
		return output
	}
	denominator := int64(outputSamples - 1)
	for i := range output {
		position := int64(i) * int64(len(input)-1)
		index := int(position / denominator)
		fraction := position % denominator
		if index >= len(input)-1 {
			output[i] = input[len(input)-1]
			continue
		}
		a := int64(input[index])
		b := int64(input[index+1])
		output[i] = clampInt16(a + (b-a)*fraction/denominator)
	}
	return output
}

func makeHalfHannQ15(samples int) []int32 {
	if samples < 2 {
		return []int32{pcmGainScale}
	}
	window := make([]int32, samples)
	for i := range window {
		gain := 0.5 - 0.5*math.Cos(math.Pi*float64(i)/float64(samples-1))
		window[i] = int32(math.Round(gain * pcmGainScale))
	}
	window[0] = 0
	window[len(window)-1] = pcmGainScale
	return window
}

func applyFadeIn(samples []int16, window []int32) {
	count := minInt(len(samples), len(window))
	for i := 0; i < count; i++ {
		samples[i] = scalePCM(samples[i], mappedWindowGain(window, i, count))
	}
}

func applyFadeOut(samples []int16, fadeIn []int32) {
	count := minInt(len(samples), len(fadeIn))
	if count == 1 {
		samples[len(samples)-1] = 0
		return
	}
	start := len(samples) - count
	for i := 0; i < count; i++ {
		samples[start+i] = scalePCM(samples[start+i], pcmGainScale-mappedWindowGain(fadeIn, i, count))
	}
}

func applyBoundarySplice(samples []int16, previous int16, fadeIn []int32) {
	count := minInt(len(samples), len(fadeIn))
	for i := 0; i < count; i++ {
		inGain := int64(mappedWindowGain(fadeIn, i, count))
		outGain := int64(pcmGainScale) - inGain
		value := (int64(previous)*outGain + int64(samples[i])*inGain) / pcmGainScale
		samples[i] = clampInt16(value)
	}
}

func applyDecayingTail(samples []int16, previous int16, fadeIn []int32) {
	count := minInt(len(samples), len(fadeIn))
	for i := 0; i < count; i++ {
		samples[i] = scalePCM(previous, pcmGainScale-mappedWindowGain(fadeIn, i, count))
	}
}

// mappedWindowGain preserves both half-Hann endpoints when fewer than the
// nominal fade samples are available. The transition then becomes shorter
// than 5 ms rather than ending with an avoidable jump into silence.
func mappedWindowGain(window []int32, position, count int) int32 {
	if len(window) == 0 || count <= 0 {
		return pcmGainScale
	}
	if count == 1 || len(window) == 1 {
		return window[0]
	}
	index := (position*(len(window)-1) + (count-1)/2) / (count - 1)
	if index >= len(window) {
		index = len(window) - 1
	}
	return window[index]
}

func scalePCM(sample int16, gain int32) int16 {
	return clampInt16(int64(sample) * int64(gain) / pcmGainScale)
}

func clampInt16(value int64) int16 {
	if value > 32767 {
		return 32767
	}
	if value < -32768 {
		return -32768
	}
	return int16(value)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
