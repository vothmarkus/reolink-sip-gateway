// Package platformaudio holds an optional process-local audio adapter supplied
// by platform hosts such as Android. Linux/HA leave it unset and retain FFmpeg.
package platformaudio

import "sync"

type Adapter interface {
	StartAACDecoder(sampleRate int32, channels int32) (int64, error)
	DecodeAAC(handle int64, adts []byte) ([]byte, error)
	StopAACDecoder(handle int64)
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
