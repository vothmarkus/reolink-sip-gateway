// Package platformaudio holds an optional process-local audio adapter supplied
// by platform hosts such as Android. Linux/HA leave it unset and retain FFmpeg.
package platformaudio

import "sync"

type Adapter interface {
	StartAACDecoder(sampleRate int32, channels int32) (int64, error)
	DecodeAAC(handle int64, adts []byte) ([]byte, error)
	StopAACDecoder(handle int64)
}

// EchoAdapter is optional so hosts that only supply AAC keep working.
// Frames use the shared native AEC1/AER1 wire layout (8 kHz mono, 10 ms).
type EchoAdapter interface {
	StartAEC(highPass, noiseSuppression bool) (int64, error)
	ProcessAEC(handle int64, request []byte) ([]byte, error)
	StopAEC(handle int64)
}

var (
	mu      sync.RWMutex
	current Adapter
)

func Current() Adapter {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Set installs one adapter and returns a restore function. The gateway supports
// one active media runtime per process, so a process-global adapter keeps the
// existing media APIs small while Android is brought up.
func Set(next Adapter) func() {
	mu.Lock()
	previous := current
	current = next
	mu.Unlock()
	return func() {
		mu.Lock()
		current = previous
		mu.Unlock()
	}
}
