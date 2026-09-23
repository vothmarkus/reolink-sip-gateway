package media

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/vothmarkus/reolink-sip-gateway/internal/platformaudio"
)

// Android runs the same AEC1/AER1 processor through JNI, without a subprocess.
type platformAECProcessor struct {
	mu      sync.Mutex
	adapter platformaudio.EchoAdapter
	handle  int64
	closed  bool
	stats   nativeAECStats
}

func newPlatformAECProcessor(adapter platformaudio.EchoAdapter, opts nativeAECOptions) (*platformAECProcessor, error) {
	handle, err := adapter.StartAEC(opts.HighPassFilter, opts.NoiseSuppression)
	if err != nil {
		return nil, fmt.Errorf("start Android WebRTC AEC: %w", err)
	}
	return &platformAECProcessor{adapter: adapter, handle: handle}, nil
}

func (p *platformAECProcessor) Process(ctx context.Context, reference, capture []int16) ([]int16, error) {
	if len(reference) != aecFrameSamples || len(capture) != aecFrameSamples {
		return nil, errors.New("Android WebRTC AEC needs 80-sample frames")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.closed {
		return nil, errors.New("Android WebRTC AEC is closed")
	}
	request := make([]byte, nativeAECRequestBytes)
	copy(request, nativeAECRequestMagic)
	for i := range reference {
		binary.LittleEndian.PutUint16(request[4+2*i:], uint16(reference[i]))
		binary.LittleEndian.PutUint16(request[4+2*aecFrameSamples+2*i:], uint16(capture[i]))
	}
	reply, err := p.adapter.ProcessAEC(p.handle, request)
	if err != nil {
		return nil, err
	}
	response, err := decodeNativeAECResponse(reply)
	if err != nil {
		return nil, err
	}
	p.stats = response.stats
	return response.pcm, nil
}

func (p *platformAECProcessor) NativeStats() nativeAECStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *platformAECProcessor) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.adapter.StopAEC(p.handle)
	}
	return nil
}
