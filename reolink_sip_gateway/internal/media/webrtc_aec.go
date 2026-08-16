package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	nativeAECHelperBinary = "/usr/bin/reolink-aec-helper"
	nativeAECRequestMagic = "AEC1"
	nativeAECReplyMagic   = "AER1"
	nativeAECRequestBytes = 4 + 2*aecFrameSamples*2
	nativeAECReplyBytes   = 4 + 4 + 4 + 5*8 + 3*4 + aecFrameSamples*2
)

// Keep these values explicit and wire-compatible with native/aec-helper/main.cc.
// Do not place them after unrelated constants in an iota block: v0.4.3 did
// exactly that, offsetting the Go bit values and making valid native statistics
// appear as <nil> even though the C++ helper had returned them.
const (
	nativeStatERL                             uint32 = 1 << 0
	nativeStatERLE                            uint32 = 1 << 1
	nativeStatDivergentFilterFraction         uint32 = 1 << 2
	nativeStatResidualEchoLikelihood          uint32 = 1 << 3
	nativeStatResidualEchoLikelihoodRecentMax uint32 = 1 << 4
	nativeStatDelayMS                         uint32 = 1 << 5
	nativeStatDelayMedianMS                   uint32 = 1 << 6
	nativeStatDelayStdDevMS                   uint32 = 1 << 7
)

type nativeAECOptions struct {
	HighPassFilter        bool
	NoiseSuppression      bool
	NoiseSuppressionLevel string
}

// nativeAECStats are the statistics reported directly by WebRTC
// AudioProcessing::GetStatistics(). They are intentionally kept separate from
// the gateway's own energy/correlation ERLE estimate so the two diagnostics can
// be compared instead of accidentally conflated.
type nativeAECStats struct {
	ValidMask                       uint32
	EchoReturnLossDB                float64
	EchoReturnLossEnhancementDB     float64
	DivergentFilterFraction         float64
	ResidualEchoLikelihood          float64
	ResidualEchoLikelihoodRecentMax float64
	DelayMS                         int32
	DelayMedianMS                   int32
	DelayStdDevMS                   int32
}

func (s nativeAECStats) has(bit uint32) bool { return s.ValidMask&bit != 0 }

type nativeAECResponse struct {
	pcm   []int16
	stats nativeAECStats
	err   error
}

type nativeAECProcessor struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	responses chan nativeAECResponse
	done      chan struct{}

	mu        sync.Mutex
	statsMu   sync.RWMutex
	stats     nativeAECStats
	stderr    *nativeAECStderrCapture
	waitErrMu sync.Mutex
	waitErr   error
	closeOnce sync.Once
}

// nativeAECStderrCapture is assigned directly to exec.Cmd.Stderr instead of
// using StderrPipe. os/exec then owns the copy goroutine and Wait() does not
// return until the diagnostic bytes have been copied into this writer. That
// avoids losing the final stderr line when the native helper exits immediately.
type nativeAECStderrCapture struct {
	mu     sync.Mutex
	buf    []byte
	logger *slog.Logger
}

func (c *nativeAECStderrCapture) Write(b []byte) (int, error) {
	if c == nil {
		return len(b), nil
	}
	c.mu.Lock()
	c.buf = append(c.buf, b...)
	const maxBytes = 8192
	if len(c.buf) > maxBytes {
		copy(c.buf, c.buf[len(c.buf)-maxBytes:])
		c.buf = c.buf[:maxBytes]
	}
	c.mu.Unlock()
	if c.logger != nil {
		if line := strings.TrimSpace(string(b)); line != "" {
			c.logger.Debug("native WebRTC AEC helper", "message", line)
		}
	}
	return len(b), nil
}

func (c *nativeAECStderrCapture) String() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimSpace(string(c.buf))
}

func newNativeAECProcessor(parent context.Context, opts nativeAECOptions, logger *slog.Logger) (*nativeAECProcessor, error) {
	return newNativeAECProcessorWithPath(parent, nativeAECHelperBinary, opts, logger, nil)
}

// newNativeAECProcessorWithPath is split out for a protocol-level fake-child
// regression test. extraArgs is nil in production.
func newNativeAECProcessorWithPath(parent context.Context, path string, opts nativeAECOptions, logger *slog.Logger, extraArgs []string) (*nativeAECProcessor, error) {
	args := append([]string{}, extraArgs...)
	args = append(args, buildNativeAECArgs(opts)...)

	ctx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(ctx, path, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return nil, err
	}
	stderr := &nativeAECStderrCapture{logger: logger}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		return nil, fmt.Errorf("start native WebRTC AEC helper: %w", err)
	}

	p := &nativeAECProcessor{
		cancel:    cancel,
		cmd:       cmd,
		stdin:     stdin,
		responses: make(chan nativeAECResponse, 8),
		done:      make(chan struct{}),
		stderr:    stderr,
	}
	stdoutDone := make(chan struct{})
	go func() {
		p.readResponses(stdout)
		close(stdoutDone)
	}()
	go func() {
		// StdoutPipe must be completely drained before Wait closes its pipe. This
		// ordering also guarantees that a final reply written immediately before
		// helper exit has reached responses before p.done can become ready.
		<-stdoutDone
		err := cmd.Wait()
		p.waitErrMu.Lock()
		p.waitErr = err
		p.waitErrMu.Unlock()
		close(p.done)
	}()
	return p, nil
}

func buildNativeAECArgs(opts nativeAECOptions) []string {
	noiseLevel := opts.NoiseSuppressionLevel
	switch noiseLevel {
	case "low", "moderate", "high", "very-high":
	default:
		noiseLevel = "moderate"
	}
	boolArg := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	return []string{
		"--high-pass=" + boolArg(opts.HighPassFilter),
		"--noise-suppression=" + boolArg(opts.NoiseSuppression),
		"--noise-level=" + noiseLevel,
	}
}

func (p *nativeAECProcessor) Process(ctx context.Context, reference, capture []int16) ([]int16, error) {
	if len(reference) != aecFrameSamples || len(capture) != aecFrameSamples {
		return nil, fmt.Errorf("native WebRTC AEC needs %d-sample frames (reference=%d capture=%d)", aecFrameSamples, len(reference), len(capture))
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	select {
	case <-p.done:
		return nil, p.processError()
	default:
	}

	request := make([]byte, nativeAECRequestBytes)
	copy(request[:4], nativeAECRequestMagic)
	off := 4
	for _, v := range reference {
		binary.LittleEndian.PutUint16(request[off:off+2], uint16(v))
		off += 2
	}
	for _, v := range capture {
		binary.LittleEndian.PutUint16(request[off:off+2], uint16(v))
		off += 2
	}
	if err := writeAll(p.stdin, request); err != nil {
		return nil, fmt.Errorf("write native WebRTC AEC frame: %w; %v", err, p.processError())
	}

	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case resp, ok := <-p.responses:
		if !ok {
			return nil, p.processError()
		}
		if resp.err != nil {
			return nil, resp.err
		}
		p.statsMu.Lock()
		p.stats = resp.stats
		p.statsMu.Unlock()
		return resp.pcm, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("native WebRTC AEC helper did not return a 10 ms frame within 500 ms")
	}
}

func (p *nativeAECProcessor) NativeStats() nativeAECStats {
	if p == nil {
		return nativeAECStats{}
	}
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.stats
}

func (p *nativeAECProcessor) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		p.cancel()
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		select {
		case <-p.done:
		case <-time.After(time.Second):
		}
	})
	return nil
}

func (p *nativeAECProcessor) readResponses(r io.Reader) {
	defer close(p.responses)
	buf := make([]byte, nativeAECReplyBytes)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				p.tryResponse(nativeAECResponse{err: fmt.Errorf("read native WebRTC AEC reply: %w", err)})
			}
			return
		}
		resp, err := decodeNativeAECResponse(buf)
		if err != nil {
			p.tryResponse(nativeAECResponse{err: err})
			return
		}
		p.tryResponse(resp)
	}
}

func (p *nativeAECProcessor) tryResponse(resp nativeAECResponse) {
	select {
	case p.responses <- resp:
	case <-p.done:
	}
}

func decodeNativeAECResponse(buf []byte) (nativeAECResponse, error) {
	if len(buf) != nativeAECReplyBytes {
		return nativeAECResponse{}, fmt.Errorf("native WebRTC AEC reply size=%d want %d", len(buf), nativeAECReplyBytes)
	}
	if string(buf[:4]) != nativeAECReplyMagic {
		return nativeAECResponse{}, fmt.Errorf("native WebRTC AEC reply has invalid magic %q", string(buf[:4]))
	}
	status := int32(binary.LittleEndian.Uint32(buf[4:8]))
	if status != 0 {
		return nativeAECResponse{}, fmt.Errorf("native WebRTC AudioProcessing returned status %d", status)
	}
	st := nativeAECStats{ValidMask: binary.LittleEndian.Uint32(buf[8:12])}
	off := 12
	readF64 := func() float64 {
		v := mathFloat64frombits(binary.LittleEndian.Uint64(buf[off : off+8]))
		off += 8
		return v
	}
	st.EchoReturnLossDB = readF64()
	st.EchoReturnLossEnhancementDB = readF64()
	st.DivergentFilterFraction = readF64()
	st.ResidualEchoLikelihood = readF64()
	st.ResidualEchoLikelihoodRecentMax = readF64()
	st.DelayMS = int32(binary.LittleEndian.Uint32(buf[off : off+4]))
	off += 4
	st.DelayMedianMS = int32(binary.LittleEndian.Uint32(buf[off : off+4]))
	off += 4
	st.DelayStdDevMS = int32(binary.LittleEndian.Uint32(buf[off : off+4]))
	off += 4
	pcm := make([]int16, aecFrameSamples)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(buf[off : off+2]))
		off += 2
	}
	return nativeAECResponse{pcm: pcm, stats: st}, nil
}

// Kept as small wrappers so the protocol parser can stay allocation-light while
// avoiding unsafe conversions.
func mathFloat64frombits(v uint64) float64 { return math.Float64frombits(v) }

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

func (p *nativeAECProcessor) processError() error {
	// stdout can reach EOF a few scheduler ticks before cmd.Wait(). Only on the
	// error path, give Wait a short bounded chance to publish the actual exit
	// status. Because stderr is an io.Writer on exec.Cmd, Wait also guarantees the
	// stderr copy is complete before p.done closes.
	select {
	case <-p.done:
	case <-time.After(50 * time.Millisecond):
	}
	p.waitErrMu.Lock()
	waitErr := p.waitErr
	p.waitErrMu.Unlock()
	stderr := p.stderr.String()
	if waitErr == nil {
		waitErr = errors.New("native WebRTC AEC helper stopped")
	}
	if stderr != "" {
		return fmt.Errorf("native WebRTC AEC helper: %w (%s)", waitErr, stderr)
	}
	return fmt.Errorf("native WebRTC AEC helper: %w", waitErr)
}

// test helper only: encode one fixed reply without duplicating the wire layout.
func encodeNativeAECResponse(status int32, st nativeAECStats, pcm []int16) []byte {
	buf := bytes.NewBuffer(make([]byte, 0, nativeAECReplyBytes))
	buf.WriteString(nativeAECReplyMagic)
	_ = binary.Write(buf, binary.LittleEndian, status)
	_ = binary.Write(buf, binary.LittleEndian, st.ValidMask)
	for _, v := range []float64{st.EchoReturnLossDB, st.EchoReturnLossEnhancementDB, st.DivergentFilterFraction, st.ResidualEchoLikelihood, st.ResidualEchoLikelihoodRecentMax} {
		_ = binary.Write(buf, binary.LittleEndian, v)
	}
	for _, v := range []int32{st.DelayMS, st.DelayMedianMS, st.DelayStdDevMS} {
		_ = binary.Write(buf, binary.LittleEndian, v)
	}
	for i := 0; i < aecFrameSamples; i++ {
		v := int16(0)
		if i < len(pcm) {
			v = pcm[i]
		}
		_ = binary.Write(buf, binary.LittleEndian, v)
	}
	return buf.Bytes()
}
