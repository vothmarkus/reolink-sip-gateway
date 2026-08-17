// Package acousticmarker generates the coded speech-band signal shared by
// startup latency calibration and the short incoming-call indication tone.
package acousticmarker

import (
	"math"
	"time"
)

const (
	SymbolDuration       = 64 * time.Millisecond
	CalibrationSymbols   = 16
	ConnectionSymbols    = 4
	CalibrationDuration  = SymbolDuration * CalibrationSymbols
	ConnectionDuration   = SymbolDuration * ConnectionSymbols
	CalibrationAmplitude = 10000.0
)

var frequencies = [...]float64{850, 1200, 1700, 2300}

// The order deliberately avoids a short periodic pattern. The connection
// indication uses the first four symbols of the exact same acoustic code, so
// it remains recognizable without replaying the full 1.024-second marker.
var calibrationCode = [...]uint8{0, 3, 1, 2, 3, 0, 2, 1, 2, 0, 3, 1, 1, 3, 0, 2}

func Frequencies() []float64 { return append([]float64(nil), frequencies[:]...) }
func CalibrationCode() []uint8 {
	return append([]uint8(nil), calibrationCode[:]...)
}

func Calibration(rate int) []int16 {
	return generate(rate, calibrationCode[:], CalibrationAmplitude)
}

func Connection(rate int) []int16 {
	return generate(rate, calibrationCode[:ConnectionSymbols], CalibrationAmplitude)
}

func generate(rate int, code []uint8, amplitude float64) []int16 {
	if rate <= 0 || len(code) == 0 {
		return nil
	}
	duration := time.Duration(len(code)) * SymbolDuration
	totalSamples := int(math.Round(duration.Seconds() * float64(rate)))
	if totalSamples < len(code)*8 {
		return nil
	}
	out := make([]int16, totalSamples)
	fade := int(math.Round(0.006 * float64(rate))) // 6 ms fade on each symbol edge.
	if fade < 1 {
		fade = 1
	}
	for symbolIndex, codeValue := range code {
		if int(codeValue) >= len(frequencies) {
			continue
		}
		start := int(math.Round(float64(symbolIndex) * SymbolDuration.Seconds() * float64(rate)))
		end := int(math.Round(float64(symbolIndex+1) * SymbolDuration.Seconds() * float64(rate)))
		if end > len(out) {
			end = len(out)
		}
		if start >= end {
			continue
		}
		symbolSamples := end - start
		symbolFade := fade
		if symbolFade*2 >= symbolSamples {
			symbolFade = symbolSamples / 4
		}
		freq := frequencies[codeValue]
		phaseStep := 2 * math.Pi * freq / float64(rate)
		phase := 0.0
		for i := 0; i < symbolSamples; i++ {
			envelope := 1.0
			if i < symbolFade {
				x := float64(i) / float64(symbolFade)
				envelope = math.Sin(x * math.Pi / 2)
				envelope *= envelope
			}
			if tail := symbolSamples - 1 - i; tail < symbolFade {
				x := float64(tail) / float64(symbolFade)
				tailEnvelope := math.Sin(x * math.Pi / 2)
				envelope *= tailEnvelope * tailEnvelope
			}
			out[start+i] = int16(math.Round(amplitude * envelope * math.Sin(phase)))
			phase += phaseStep
		}
	}
	return out
}
