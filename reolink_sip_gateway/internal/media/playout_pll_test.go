package media

import (
	"math"
	"testing"
	"time"
)

func TestCameraPlayoutSmootherPrebuffersThenProducesExactSIPFrames(t *testing.T) {
	p := newCameraPlayoutPLL()
	base := time.Unix(1000, 0)
	p.Push(make([]int16, cameraPlayoutStartupSamples-1), base)
	if p.Ready() {
		t.Fatal("playout became ready below startup watermark")
	}
	p.Push([]int16{1}, base.Add(time.Millisecond))
	if !p.Ready() {
		t.Fatal("playout did not become ready at startup watermark")
	}
	frame, _, _, ready := p.PopFrame(base.Add(20 * time.Millisecond))
	if !ready || len(frame) != cameraPlayoutFrameSamples {
		t.Fatalf("ready=%t frame=%d want %d", ready, len(frame), cameraPlayoutFrameSamples)
	}
}

func TestCameraPlayoutSmootherHandlesClusteredAACWithoutEdgeChurn(t *testing.T) {
	p := newCameraPlayoutPLL()
	base := time.Unix(1000, 0)
	phaseValue := 0.0
	// Model an intentionally harsh decoder pattern: four 80 ms reads arrive in
	// a tight cluster every 320 ms. v0.4.1's 120 ms hard ceiling necessarily hit
	// both overflow and underrun edges under this pattern.
	for cycle := 0; cycle < 120; cycle++ { // 38.4 seconds
		arrival := base.Add(time.Duration(cycle) * 320 * time.Millisecond)
		for burst := 0; burst < 4; burst++ {
			chunk := make([]int16, 640)
			for i := range chunk {
				phaseValue += 0.03
				chunk[i] = int16(math.Sin(phaseValue) * 12000)
			}
			p.Push(chunk, arrival.Add(time.Duration(burst)*time.Millisecond))
		}
		for tick := 0; tick < 16; tick++ {
			now := arrival.Add(time.Duration(tick+1) * 20 * time.Millisecond)
			frame, missing, _, ready := p.PopFrame(now)
			if !ready || len(frame) != 160 {
				t.Fatalf("cycle=%d tick=%d ready=%t frame=%d", cycle, tick, ready, len(frame))
			}
			if missing > 2 {
				t.Fatalf("cycle=%d tick=%d excessive underrun=%d", cycle, tick, missing)
			}
		}
	}
	st := p.Stats()
	if st.HardDroppedSamples != 0 {
		t.Fatalf("clustered AAC caused hard drops: %+v", st)
	}
	if st.UnderrunOutputSamples > 32 {
		t.Fatalf("clustered AAC caused excessive underrun: %+v", st)
	}
	if st.QueueMaxSamples > cameraPlayoutMaxSamples {
		t.Fatalf("queue max=%d exceeds %d", st.QueueMaxSamples, cameraPlayoutMaxSamples)
	}
	if math.Abs(st.AverageCorrectionPPM) > 1250.1 || st.MaximumCorrectionPPM > 1250.1 {
		t.Fatalf("ASRC escaped slow-drift bound: %+v", st)
	}
}

func TestCameraPlayoutVirtualMediaClockIgnoresDecoderBurstArrival(t *testing.T) {
	p := newCameraPlayoutPLL()
	base := time.Unix(2000, 0)
	// Two 80 ms chunks arrive only 1 ms apart. Their media time must nevertheless
	// be contiguous rather than inheriting that 1 ms arrival spacing.
	p.Push(make([]int16, 640), base.Add(80*time.Millisecond))
	p.Push(make([]int16, 640), base.Add(81*time.Millisecond))
	if !p.Ready() {
		t.Fatal("smoother not ready")
	}
	_, _, t0, _ := p.PopFrame(base.Add(100 * time.Millisecond))
	_, _, t1, _ := p.PopFrame(base.Add(120 * time.Millisecond))
	d := t1.Sub(t0)
	if d < 19*time.Millisecond || d > 21*time.Millisecond {
		t.Fatalf("virtual media clock step=%s want about 20ms", d)
	}
}

func TestCameraPlayoutSmootherHardOverflowRecoversBelowCeiling(t *testing.T) {
	p := newCameraPlayoutPLL()
	big := make([]int16, cameraPlayoutMaxSamples+800)
	dropped, rebase := p.Push(big, time.Unix(1000, 0))
	wantDrop := len(big) - cameraPlayoutRecoverySamples
	if dropped != wantDrop || !rebase {
		t.Fatalf("dropped=%d rebase=%t want=%d/true", dropped, rebase, wantDrop)
	}
	if p.Len() != cameraPlayoutRecoverySamples {
		t.Fatalf("queue=%d want recovery=%d", p.Len(), cameraPlayoutRecoverySamples)
	}
	if p.Stats().HardDroppedSamples != uint64(wantDrop) {
		t.Fatalf("stats hard drops=%d", p.Stats().HardDroppedSamples)
	}
}

func TestCameraPlayoutSmootherUnderrunReturnsSilenceAndCounts(t *testing.T) {
	p := newCameraPlayoutPLL()
	base := time.Unix(1000, 0)
	p.Push(make([]int16, cameraPlayoutStartupSamples), base)
	for i := 0; i < 20; i++ {
		frame, _, _, ready := p.PopFrame(base.Add(time.Duration(i+1) * 20 * time.Millisecond))
		if !ready || len(frame) != 160 {
			t.Fatalf("frame %d invalid", i)
		}
	}
	_, missing, _, _ := p.PopFrame(base.Add(500 * time.Millisecond))
	if missing == 0 || p.Stats().UnderrunOutputSamples == 0 {
		t.Fatalf("expected counted underrun, missing=%d stats=%+v", missing, p.Stats())
	}
}

func TestCameraPlayoutMediaClockNeverMovesBackwardAfterUnderrunRefill(t *testing.T) {
	p := newCameraPlayoutPLL()
	base := time.Unix(5000, 0)
	p.Push(make([]int16, cameraPlayoutStartupSamples), base)
	var last time.Time
	// Drain past the buffered media so at least one synthetic-silence frame is emitted.
	for i := 0; i < 10; i++ {
		_, _, at, ready := p.PopFrame(base.Add(time.Duration(i+1) * 20 * time.Millisecond))
		if !ready {
			t.Fatalf("frame %d not ready", i)
		}
		if !last.IsZero() && !at.After(last) {
			t.Fatalf("media clock did not advance before refill: %s -> %s", last, at)
		}
		last = at
	}
	// Refill arrives late, but its old inputNextAt must not rewind capture time.
	_, rebase := p.Push(make([]int16, 640), base.Add(400*time.Millisecond))
	if !rebase {
		t.Fatal("late refill did not report timeline rebase")
	}
	_, _, at, ready := p.PopFrame(base.Add(420 * time.Millisecond))
	if !ready {
		t.Fatal("refilled playout not ready")
	}
	if !at.After(last) {
		t.Fatalf("media clock moved backward after refill: %s -> %s", last, at)
	}
}

func TestCameraPlayoutQueueAverageCountsPushObservations(t *testing.T) {
	p := newCameraPlayoutPLL()
	base := time.Unix(1000, 0)
	p.Push(make([]int16, 100), base)
	p.Push(make([]int16, 100), base.Add(time.Millisecond))
	st := p.Stats()
	if math.Abs(st.QueueAverageSamples-150) > 0.001 {
		t.Fatalf("queue average=%.3f want 150.0", st.QueueAverageSamples)
	}
}
