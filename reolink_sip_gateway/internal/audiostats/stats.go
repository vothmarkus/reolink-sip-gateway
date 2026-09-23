// Package audiostats records the camera-to-phone stages without storing audio.
package audiostats

import "sync"

// Snapshot is comparable so status stores can suppress unchanged updates.
type Snapshot struct {
	Available    bool   `json:"available"`
	Packets      uint64 `json:"packets"`
	EncodedBytes uint64 `json:"encoded_bytes"`
	PCMSamples   uint64 `json:"pcm_samples"`
	PCMPeak      int    `json:"pcm_peak"`
	RTPPackets   uint64 `json:"rtp_packets"`
}

type Counters struct {
	mu    sync.Mutex
	value Snapshot
}

func (c *Counters) Received(size int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value.Packets++
	c.value.EncodedBytes += uint64(size)
}

func (c *Counters) Decoded(pcm []int16) {
	if c == nil {
		return
	}
	peak := 0
	for _, sample := range pcm {
		v := int(sample)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value.PCMSamples += uint64(len(pcm))
	if peak > c.value.PCMPeak {
		c.value.PCMPeak = peak
	}
}

func (c *Counters) SentRTP() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value.RTPPackets++
}

func (c *Counters) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.value
	s.Available = true
	return s
}
