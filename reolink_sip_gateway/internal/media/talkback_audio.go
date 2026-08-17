package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/codec"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
)

const (
	g711SampleRate       = 8000
	maxInputGapSamples   = 1600 // 200 ms at 8 kHz; larger jumps reset the media timeline.
	defaultReorderWindow = 3
	reorderFlushDelay    = 40 * time.Millisecond
)

type talkBlockWriter interface {
	WriteADPCMBlock(context.Context, []byte) error
}

type audioBridgeStats struct {
	PacketsAccepted          uint64
	PacketsDuplicateLate     uint64
	PacketsReordered         uint64
	SequenceGaps             uint64
	TimelineResets           uint64
	SilenceInsertedInput     uint64
	FIFORawShortage          uint64
	FIFOOverflowSamples      uint64
	FIFOUnderrunSamples      uint64
	FIFOMaxSamples           uint64
	FIFOPlayoutMin           uint64
	FIFOPlayoutMax           uint64
	FIFOPlayoutTotal         uint64
	FIFOPlayoutBlocks        uint64
	ElasticStretchBlocks     uint64
	ElasticCompressBlocks    uint64
	ElasticStretchedSamples  uint64
	ElasticCompressedSamples uint64
	ElasticRatioMinPPM       uint64
	ElasticRatioMaxPPM       uint64
	ElasticCurrentRatioPPM   uint64
	ElasticFadeOuts          uint64
	ElasticFadeIns           uint64
	ElasticOverflowSplices   uint64
	ElasticSupplyTrend       float64
	SSRCChanges              uint64
	RTPJitterMaxUS           uint64
	BaichuanBlocks           uint64
	BaichuanWriteTotalUS     uint64
	BaichuanWriteMaxUS       uint64
}

type sampleFIFO struct {
	buf []int16
	max int
}

func newSampleFIFO(max int) *sampleFIFO {
	if max < 1 {
		max = 1
	}
	return &sampleFIFO{max: max}
}

func (f *sampleFIFO) Reset()   { f.buf = f.buf[:0] }
func (f *sampleFIFO) Len() int { return len(f.buf) }

func (f *sampleFIFO) Pop(n int) []int16 {
	if n <= 0 || len(f.buf) == 0 {
		return nil
	}
	if n > len(f.buf) {
		n = len(f.buf)
	}
	out := append([]int16(nil), f.buf[:n]...)
	copy(f.buf, f.buf[n:])
	f.buf = f.buf[:len(f.buf)-n]
	return out
}

func (f *sampleFIFO) Push(samples []int16) (dropped int) {
	if len(samples) == 0 {
		return 0
	}
	if len(samples) >= f.max {
		dropped = len(f.buf) + len(samples) - f.max
		f.buf = append(f.buf[:0], samples[len(samples)-f.max:]...)
		return dropped
	}
	if overflow := len(f.buf) + len(samples) - f.max; overflow > 0 {
		dropped = overflow
		copy(f.buf, f.buf[overflow:])
		f.buf = f.buf[:len(f.buf)-overflow]
	}
	f.buf = append(f.buf, samples...)
	return dropped
}

// linearResampler performs streaming linear interpolation from 8 kHz G.711
// PCM to the rate negotiated by the Reolink talk profile. It keeps one source
// sample of history so interpolation remains continuous across RTP packets.
type linearResampler struct {
	outRate     int
	havePrev    bool
	prev        int16
	sourceIndex int64
	nextOutTick int64
}

func newLinearResampler(outRate int) (*linearResampler, error) {
	if outRate < g711SampleRate || outRate > 48000 {
		return nil, fmt.Errorf("unsupported Reolink talk sample rate %d Hz", outRate)
	}
	return &linearResampler{outRate: outRate}, nil
}

func (r *linearResampler) Reset() {
	r.havePrev = false
	r.prev = 0
	r.sourceIndex = 0
	r.nextOutTick = 0
}

func (r *linearResampler) Push(in []int16) []int16 {
	if len(in) == 0 {
		return nil
	}
	if r.outRate == g711SampleRate {
		out := append([]int16(nil), in...)
		return out
	}
	// Upper bound for supported upsampling ratios plus a small boundary margin.
	capHint := len(in)*r.outRate/g711SampleRate + 4
	out := make([]int16, 0, capHint)
	for _, cur := range in {
		if !r.havePrev {
			r.prev = cur
			r.havePrev = true
			r.sourceIndex = 0
			r.nextOutTick = 0
			continue
		}
		startTick := r.sourceIndex * int64(r.outRate)
		r.sourceIndex++
		endTick := r.sourceIndex * int64(r.outRate)
		for r.nextOutTick < endTick {
			num := r.nextOutTick - startTick
			if num < 0 {
				num = 0
			}
			delta := int64(cur) - int64(r.prev)
			value := int64(r.prev) + delta*num/int64(r.outRate)
			if value > 32767 {
				value = 32767
			} else if value < -32768 {
				value = -32768
			}
			out = append(out, int16(value))
			r.nextOutTick += g711SampleRate
		}
		r.prev = cur
	}
	return out
}

type sequencedPacket struct {
	packet rtp.Packet
	reset  bool
}

type rtpSequencer struct {
	initialized bool
	ssrc        uint32
	expected    uint16
	pending     map[uint16]rtp.Packet
	window      int
	stats       audioBridgeStats
}

func newRTPSequencer(window int) *rtpSequencer {
	if window < 1 {
		window = 1
	}
	return &rtpSequencer{window: window, pending: make(map[uint16]rtp.Packet)}
}

func (q *rtpSequencer) resetAt(p rtp.Packet, ssrcChanged bool) []sequencedPacket {
	q.pending = make(map[uint16]rtp.Packet)
	q.ssrc = p.SSRC
	q.expected = p.Sequence
	q.initialized = true
	if ssrcChanged {
		q.stats.SSRCChanges++
	}
	q.pending[p.Sequence] = p
	return q.drain(true)
}

func (q *rtpSequencer) Push(p rtp.Packet) []sequencedPacket {
	if !q.initialized {
		return q.resetAt(p, false)
	}
	if p.SSRC != q.ssrc {
		return q.resetAt(p, true)
	}
	if _, exists := q.pending[p.Sequence]; exists {
		q.stats.PacketsDuplicateLate++
		return nil
	}
	diff := int16(p.Sequence - q.expected)
	if diff < 0 {
		q.stats.PacketsDuplicateLate++
		return nil
	}
	if diff > int16(q.window*8) {
		// A large sequence jump is treated as a new timeline rather than
		// synthesizing seconds of stale audio.
		q.stats.SequenceGaps++
		q.stats.TimelineResets++
		return q.resetAt(p, false)
	}
	if diff > 0 {
		q.stats.PacketsReordered++
	}
	q.pending[p.Sequence] = p
	if diff == 0 {
		return q.drain(false)
	}
	if len(q.pending) >= q.window {
		return q.FlushGap()
	}
	return nil
}

func (q *rtpSequencer) FlushGap() []sequencedPacket {
	if !q.initialized || len(q.pending) == 0 {
		return nil
	}
	bestSet := false
	var best uint16
	var bestDiff int16
	for seq := range q.pending {
		d := int16(seq - q.expected)
		if d < 0 {
			delete(q.pending, seq)
			continue
		}
		if !bestSet || d < bestDiff {
			bestSet = true
			best = seq
			bestDiff = d
		}
	}
	if !bestSet {
		return nil
	}
	gap := best != q.expected
	if gap {
		q.expected = best
		q.stats.SequenceGaps++
	}
	return q.drain(false)
}

func (q *rtpSequencer) Stats() audioBridgeStats { return q.stats }

func (q *rtpSequencer) drain(resetFirst bool) []sequencedPacket {
	var out []sequencedPacket
	first := true
	for {
		p, ok := q.pending[q.expected]
		if !ok {
			break
		}
		delete(q.pending, q.expected)
		out = append(out, sequencedPacket{packet: p, reset: resetFirst && first})
		q.expected++
		first = false
	}
	return out
}

type phoneAudioBuffer struct {
	mu            sync.Mutex
	codecName     string
	resampler     *linearResampler
	fifo          *sampleFIFO
	elastic       *elasticTalkbackPlayout
	stats         audioBridgeStats
	haveTimeline  bool
	nextTimestamp uint32
	receivedAudio bool
}

func newPhoneAudioBuffer(codecName string, outputRate, blockSamples int) (*phoneAudioBuffer, error) {
	if codecName != g711.PCMA && codecName != g711.PCMU {
		return nil, fmt.Errorf("unsupported SIP talkback codec %q", codecName)
	}
	r, err := newLinearResampler(outputRate)
	if err != nil {
		return nil, err
	}
	// Four camera blocks absorb short network bursts while bounding queued
	// talkback latency to about 256 ms for the observed 16 kHz / 1024-sample
	// Reolink profile. On overflow the oldest samples are dropped deliberately
	// so latency cannot grow without bound.
	return &phoneAudioBuffer{
		codecName: codecName,
		resampler: r,
		fifo:      newSampleFIFO(blockSamples * 4),
		elastic:   newElasticTalkbackPlayout(outputRate),
	}, nil
}

func (b *phoneAudioBuffer) resetLocked() {
	b.haveTimeline = false
	b.nextTimestamp = 0
	b.resampler.Reset()
	b.fifo.Reset()
	b.elastic.ResetTimeline()
}

func (b *phoneAudioBuffer) Push(frame sequencedPacket) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if frame.reset {
		b.resetLocked()
		b.stats.TimelineResets++
	}
	p := frame.packet
	payload := p.Payload
	if len(payload) == 0 {
		return
	}
	if b.haveTimeline {
		delta := int32(p.Timestamp - b.nextTimestamp)
		switch {
		case delta > 0 && delta <= maxInputGapSamples:
			b.pushPCM8Locked(make([]int16, int(delta)))
			b.stats.SilenceInsertedInput += uint64(delta)
		case delta > maxInputGapSamples:
			b.resetLocked()
			b.stats.TimelineResets++
		case delta < 0:
			overlap := int(-delta)
			if overlap >= len(payload) {
				b.stats.PacketsDuplicateLate++
				return
			}
			payload = payload[overlap:]
			p.Timestamp += uint32(overlap)
		}
	}
	pcm := g711.DecodePayload(payload, b.codecName)
	if len(pcm) == 0 {
		return
	}
	b.pushPCM8Locked(pcm)
	b.haveTimeline = true
	b.nextTimestamp = p.Timestamp + uint32(len(payload))
	b.receivedAudio = true
	b.stats.PacketsAccepted++
}

func (b *phoneAudioBuffer) pushPCM8Locked(pcm []int16) {
	resampled := b.resampler.Push(pcm)
	if dropped := b.fifo.Push(resampled); dropped > 0 {
		b.stats.FIFOOverflowSamples += uint64(dropped)
		b.elastic.MarkOverflow()
	}
	if n := uint64(b.fifo.Len()); n > b.stats.FIFOMaxSamples {
		b.stats.FIFOMaxSamples = n
	}
}

func (b *phoneAudioBuffer) PopBlock(n int) []int16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := b.elastic.Pop(b.fifo, n)
	if b.receivedAudio {
		b.stats.FIFORawShortage += uint64(result.rawShortage)
		b.stats.FIFOUnderrunSamples += uint64(result.missing)
		b.stats.FIFOPlayoutBlocks++
		b.stats.FIFOPlayoutTotal += uint64(result.depthBefore)
		if b.stats.FIFOPlayoutBlocks == 1 || uint64(result.depthBefore) < b.stats.FIFOPlayoutMin {
			b.stats.FIFOPlayoutMin = uint64(result.depthBefore)
		}
		if uint64(result.depthBefore) > b.stats.FIFOPlayoutMax {
			b.stats.FIFOPlayoutMax = uint64(result.depthBefore)
		}
		if result.stretched > 0 {
			b.stats.ElasticStretchBlocks++
			b.stats.ElasticStretchedSamples += uint64(result.stretched)
		}
		if result.compressed > 0 {
			b.stats.ElasticCompressBlocks++
			b.stats.ElasticCompressedSamples += uint64(result.compressed)
		}
		if result.consumed > 0 && result.validOutput > 0 {
			ratio := uint64(result.ratioPPM)
			b.stats.ElasticCurrentRatioPPM = ratio
			if b.stats.ElasticRatioMinPPM == 0 || ratio < b.stats.ElasticRatioMinPPM {
				b.stats.ElasticRatioMinPPM = ratio
			}
			if ratio > b.stats.ElasticRatioMaxPPM {
				b.stats.ElasticRatioMaxPPM = ratio
			}
		}
		if result.fadeOut {
			b.stats.ElasticFadeOuts++
		}
		if result.fadeIn {
			b.stats.ElasticFadeIns++
		}
		if result.overflowSplice {
			b.stats.ElasticOverflowSplices++
		}
		b.stats.ElasticSupplyTrend = result.supplyTrend
	}
	return result.block
}

func (b *phoneAudioBuffer) Stats() audioBridgeStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

func (b *phoneAudioBuffer) MergeSequencerStats(st audioBridgeStats) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stats.PacketsDuplicateLate += st.PacketsDuplicateLate
	b.stats.PacketsReordered += st.PacketsReordered
	b.stats.SequenceGaps += st.SequenceGaps
	b.stats.TimelineResets += st.TimelineResets
	b.stats.SSRCChanges += st.SSRCChanges
}

func (b *phoneAudioBuffer) ObserveRTPJitter(d time.Duration) {
	if d < 0 {
		d = -d
	}
	us := uint64(d / time.Microsecond)
	b.mu.Lock()
	if us > b.stats.RTPJitterMaxUS {
		b.stats.RTPJitterMaxUS = us
	}
	b.mu.Unlock()
}

func (b *phoneAudioBuffer) RecordBaichuanWrite(d time.Duration) {
	if d < 0 {
		d = 0
	}
	us := uint64(d / time.Microsecond)
	b.mu.Lock()
	b.stats.BaichuanBlocks++
	b.stats.BaichuanWriteTotalUS += us
	if us > b.stats.BaichuanWriteMaxUS {
		b.stats.BaichuanWriteMaxUS = us
	}
	b.mu.Unlock()
}

func runBaichuanAudioBridge(ctx context.Context, conn *net.UDPConn, call *sip.Call, writer talkBlockWriter, outputRate, blockSamples int, controls *audioControls, logger *slog.Logger, peerDone <-chan struct{}, peerErr func() error, rtpInactivity time.Duration) error {
	if outputRate <= 0 || blockSamples <= 0 {
		return errors.New("invalid Baichuan audio profile")
	}
	bridge, err := newPhoneAudioBuffer(call.Codec.Name, outputRate, blockSamples)
	if err != nil {
		return err
	}
	sequencer := newRTPSequencer(defaultReorderWindow)
	bridgeCtx, cancelBridge := context.WithCancel(ctx)
	defer cancelBridge()
	recvErr := make(chan error, 1)
	go func() {
		err := receivePhoneRTP(bridgeCtx, conn, call, bridge, sequencer, rtpInactivity)
		bridge.MergeSequencerStats(sequencer.Stats())
		recvErr <- err
	}()
	receiverFinished := false

	blockDuration := time.Duration(int64(time.Second) * int64(blockSamples) / int64(outputRate))
	if blockDuration <= 0 {
		return errors.New("invalid Baichuan block duration")
	}
	nextSend := time.Now().Add(blockDuration)
	timer := time.NewTimer(time.Until(nextSend))
	defer timer.Stop()
	encoder := &codec.ADPCMEncoder{}
	defer func() {
		cancelBridge()
		if !receiverFinished {
			select {
			case <-recvErr:
			case <-time.After(300 * time.Millisecond):
			}
		}
		if logger != nil {
			st := bridge.Stats()
			avgWriteMS := 0.0
			if st.BaichuanBlocks > 0 {
				avgWriteMS = float64(st.BaichuanWriteTotalUS) / float64(st.BaichuanBlocks) / 1000.0
			}
			avgFIFOPlayoutMS := 0.0
			if st.FIFOPlayoutBlocks > 0 {
				avgFIFOPlayoutMS = float64(st.FIFOPlayoutTotal) / float64(st.FIFOPlayoutBlocks) * 1000.0 / float64(outputRate)
			}
			minimumRatio := st.ElasticRatioMinPPM
			maximumRatio := st.ElasticRatioMaxPPM
			currentRatio := st.ElasticCurrentRatioPPM
			if minimumRatio == 0 {
				minimumRatio = elasticRatioScale
			}
			if maximumRatio == 0 {
				maximumRatio = elasticRatioScale
			}
			if currentRatio == 0 {
				currentRatio = elasticRatioScale
			}
			logger.Debug("Baichuan live audio bridge stopped",
				"packets", st.PacketsAccepted,
				"late_or_duplicate", st.PacketsDuplicateLate,
				"reordered", st.PacketsReordered,
				"sequence_gaps", st.SequenceGaps,
				"ssrc_changes", st.SSRCChanges,
				"timeline_resets", st.TimelineResets,
				"silence_input_samples", st.SilenceInsertedInput,
				"fifo_raw_shortage_samples", st.FIFORawShortage,
				"fifo_overflow_samples", st.FIFOOverflowSamples,
				"fifo_underrun_samples", st.FIFOUnderrunSamples,
				"fifo_max_ms", float64(st.FIFOMaxSamples)*1000.0/float64(outputRate),
				"fifo_playout_min_ms", float64(st.FIFOPlayoutMin)*1000.0/float64(outputRate),
				"fifo_playout_avg_ms", avgFIFOPlayoutMS,
				"fifo_playout_max_ms", float64(st.FIFOPlayoutMax)*1000.0/float64(outputRate),
				"elastic_stretch_blocks", st.ElasticStretchBlocks,
				"elastic_compress_blocks", st.ElasticCompressBlocks,
				"elastic_stretched_samples", st.ElasticStretchedSamples,
				"elastic_compressed_samples", st.ElasticCompressedSamples,
				"elastic_ratio_min", float64(minimumRatio)/elasticRatioScale,
				"elastic_ratio_current", float64(currentRatio)/elasticRatioScale,
				"elastic_ratio_max", float64(maximumRatio)/elasticRatioScale,
				"elastic_supply_trend_samples", st.ElasticSupplyTrend,
				"elastic_fade_outs", st.ElasticFadeOuts,
				"elastic_fade_ins", st.ElasticFadeIns,
				"elastic_overflow_splices", st.ElasticOverflowSplices,
				"rtp_jitter_max_ms", float64(st.RTPJitterMaxUS)/1000.0,
				"baichuan_blocks", st.BaichuanBlocks,
				"baichuan_write_avg_ms", avgWriteMS,
				"baichuan_write_max_ms", float64(st.BaichuanWriteMaxUS)/1000.0)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-recvErr:
			receiverFinished = true
			return err
		case <-peerDone:
			if peerErr != nil {
				if err := peerErr(); err != nil && !errors.Is(err, context.Canceled) {
					return fmt.Errorf("Baichuan connection lost: %w", err)
				}
			}
			return errors.New("Baichuan connection closed")
		case <-timer.C:
			pcm := bridge.PopBlock(blockSamples)
			adpcm, err := encoder.EncodeBlock(pcm)
			if err != nil {
				return fmt.Errorf("encode Baichuan ADPCM: %w", err)
			}
			writeStarted := time.Now()
			if err := writer.WriteADPCMBlock(ctx, adpcm); err != nil {
				return fmt.Errorf("write Baichuan talk audio: %w", err)
			}
			// The AEC reference must represent what was actually handed to the
			// doorbell transport, at the moment it was handed off. This includes
			// RTP jitter, inserted silence, FIFO drops and ADPCM quantisation.
			if controls != nil && controls.NeedsRenderReference() {
				controls.ObserveBaichuanPlayout(adpcm, outputRate, writeStarted)
			}
			bridge.RecordBaichuanWrite(time.Since(writeStarted))

			nextSend = nextSend.Add(blockDuration)
			now := time.Now()
			if !nextSend.After(now) {
				// Never burst old blocks after a scheduling or network stall. A
				// fresh deadline preserves real-time behaviour and the bounded FIFO
				// drops stale samples if the producer has moved too far ahead.
				nextSend = now.Add(blockDuration)
			}
			timer.Reset(time.Until(nextSend))
		}
	}
}

func receivePhoneRTP(ctx context.Context, conn *net.UDPConn, call *sip.Call, bridge *phoneAudioBuffer, sequencer *rtpSequencer, rtpInactivity time.Duration) error {
	buf := make([]byte, 4096)
	remote := call.RemoteRTPAddr()
	if remote == nil {
		return errors.New("missing remote RTP address")
	}
	remoteIP := append(net.IP(nil), remote.IP...)
	watchdog := newRTPWatchdog(rtpInactivity, time.Now())
	lastPacket := time.Now()
	var previousArrival time.Time
	var previousTimestamp uint32
	var previousSSRC uint32
	for {
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if watchdogErr := watchdog.Check(time.Now()); watchdogErr != nil {
					return watchdogErr
				}
				if time.Since(lastPacket) >= reorderFlushDelay {
					for _, frame := range sequencer.FlushGap() {
						bridge.Push(frame)
					}
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					continue
				}
			}
			return err
		}
		if !addr.IP.Equal(remoteIP) {
			continue
		}
		p, err := rtp.Parse(buf[:n])
		if err != nil || len(p.Payload) == 0 || p.PayloadType != call.Codec.PayloadType {
			continue
		}
		arrival := time.Now()
		if !previousArrival.IsZero() && p.SSRC == previousSSRC {
			tsDelta := int32(p.Timestamp - previousTimestamp)
			if tsDelta > 0 && tsDelta <= g711SampleRate*2 {
				expected := time.Duration(int64(tsDelta)) * time.Second / g711SampleRate
				bridge.ObserveRTPJitter(arrival.Sub(previousArrival) - expected)
			}
		}
		previousArrival = arrival
		previousTimestamp = p.Timestamp
		previousSSRC = p.SSRC
		// Only a syntactically valid RTP packet with the negotiated payload type
		// may retarget symmetric RTP. This avoids changing the media destination
		// because of malformed/RTCP/unrelated UDP traffic from the PBX host.
		current := call.RemoteRTPAddr()
		if current == nil || addr.Port != current.Port { // RFC 4961-style symmetric RTP adaptation.
			call.UpdateRemoteRTP(addr)
		}
		lastPacket = time.Now()
		watchdog.Mark(lastPacket)
		for _, frame := range sequencer.Push(p) {
			bridge.Push(frame)
		}
	}
}
