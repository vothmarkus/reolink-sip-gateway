// Package baichuanaudio exposes the audio portion of Reolink's proprietary
// Baichuan preview stream as linear PCM. It deliberately keeps the proprietary
// transport separate from SIP/RTP so it can be reused by live calls and by the
// acoustic latency self-test.
package baichuanaudio

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/audiostats"
	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
	"github.com/vothmarkus/reolink-sip-gateway/internal/codec"
	"github.com/vothmarkus/reolink-sip-gateway/internal/platformaudio"
)

const (
	defaultADPCMSampleRate = 16000
	maxPCMQueueChunks      = 8
)

type Config struct {
	Host       string
	Port       int
	Username   string
	Password   string
	Channel    uint8
	Stream     baichuan.Stream
	OutputRate int
	FFmpegPath string
	Logger     *slog.Logger
	Debug      bool
	Stats      *audiostats.Counters
}

type Info struct {
	Codec            string
	InputSampleRate  int
	OutputSampleRate int
	Channel          uint8
	Stream           baichuan.Stream
}

func (i Info) Details() string {
	inRate := "unknown rate"
	if i.InputSampleRate > 0 {
		inRate = fmt.Sprintf("%d Hz", i.InputSampleRate)
	}
	return fmt.Sprintf("Baichuan %s channel %d, %s %s -> PCM %d Hz", i.Stream.ShortName(), i.Channel, strings.ToUpper(i.Codec), inRate, i.OutputSampleRate)
}

type Receiver struct {
	pcm    chan []int16
	ready  chan Info
	done   chan error
	cancel context.CancelFunc
	client *baichuan.Client
	reader *baichuan.MediaReader
	once   sync.Once
	wg     sync.WaitGroup
}

func Open(parent context.Context, cfg Config) (*Receiver, error) {
	if cfg.OutputRate < 8000 || cfg.OutputRate > 48000 {
		return nil, fmt.Errorf("unsupported Baichuan PCM output rate %d", cfg.OutputRate)
	}
	if cfg.Stream == "" {
		cfg.Stream = baichuan.StreamSub
	}
	ctx, cancel := context.WithCancel(parent)
	client, err := baichuan.Dial(ctx, baichuan.Config{
		Host: cfg.Host, Port: cfg.Port, Username: cfg.Username, Password: cfg.Password, Timeout: 10 * time.Second,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connect Baichuan preview: %w", err)
	}

	// Legacy ADPCM media frames do not encode a sample rate in each bcmedia
	// packet. Reolink advertises the usable rate in TalkAbility on devices that
	// support talkback, so use that as a best-effort hint. If the command is not
	// available, 16 kHz is the conservative fallback used by current doorbells.
	adpcmRate := defaultADPCMSampleRate
	profileCtx, profileCancel := context.WithTimeout(ctx, 3*time.Second)
	if profile, profileErr := client.PreferredTalkAudioProfile(profileCtx, cfg.Channel); profileErr == nil && profile.SampleRate >= 8000 && profile.SampleRate <= 48000 {
		adpcmRate = int(profile.SampleRate)
	} else if profileErr != nil && cfg.Debug && cfg.Logger != nil {
		cfg.Logger.Debug("Baichuan receive sample-rate hint unavailable; using fallback", "fallback_hz", adpcmRate, "error", profileErr)
	}
	profileCancel()

	reader, err := client.StartAudioPreview(ctx, cfg.Channel, cfg.Stream)
	if err != nil {
		_ = client.Close()
		cancel()
		return nil, fmt.Errorf("start Baichuan preview: %w", err)
	}
	r := &Receiver{
		pcm: make(chan []int16, maxPCMQueueChunks), ready: make(chan Info, 1), done: make(chan error, 1),
		cancel: cancel, client: client, reader: reader,
	}
	r.wg.Add(1)
	go r.run(ctx, cfg, adpcmRate)
	return r, nil
}

func (r *Receiver) PCM() <-chan []int16 { return r.pcm }
func (r *Receiver) Ready() <-chan Info  { return r.ready }
func (r *Receiver) Done() <-chan error  { return r.done }

func (r *Receiver) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.cancel()
		if r.reader != nil {
			r.reader.Close()
		}
		if r.client != nil {
			_ = r.client.Close()
		}
		r.wg.Wait()
	})
}

func (r *Receiver) run(ctx context.Context, cfg Config, adpcmRate int) {
	defer r.wg.Done()
	r.receive(ctx, cfg, adpcmRate, r.reader.Packets, r.client.Done(), r.client.Err)
}

func (r *Receiver) receive(ctx context.Context, cfg Config, adpcmRate int, packets <-chan baichuan.MediaPacket, disconnected <-chan struct{}, connectionErr func() error) {
	defer close(r.pcm)
	defer close(r.ready)
	var terminalErr error
	defer func() {
		if terminalErr == nil && ctx.Err() != nil {
			terminalErr = ctx.Err()
		}
		r.done <- terminalErr
		close(r.done)
	}()

	var activeCodec string
	var decoder codec.ADPCMDecoder
	var aac *aacDecoder
	var platformAAC *platformAACDecoder
	var aacRate int
	var ready bool
	var aacPCM <-chan []int16
	var aacDone <-chan error
	defer func() {
		if aac != nil {
			aac.Close()
		}
		if platformAAC != nil {
			platformAAC.Close()
		}
	}()

	signalReady := func(codecName string, inputRate int) {
		if ready {
			return
		}
		ready = true
		info := Info{Codec: codecName, InputSampleRate: inputRate, OutputSampleRate: cfg.OutputRate, Channel: cfg.Channel, Stream: cfg.Stream}
		select {
		case r.ready <- info:
		default:
		}
		if cfg.Logger != nil {
			cfg.Logger.Info("Baichuan receive audio active", "channel", cfg.Channel, "stream", cfg.Stream.ShortName(), "codec", codecName, "input_sample_rate", inputRate, "output_sample_rate", cfg.OutputRate)
		}
	}

	emit := func(pcm []int16, codecName string, inputRate int) bool {
		if len(pcm) == 0 {
			return true
		}
		cfg.Stats.Decoded(pcm)
		// A parsed AAC header is not evidence of successful decoding. Signal
		// readiness only once actual PCM can be consumed by the media worker.
		signalReady(codecName, inputRate)
		return r.emitPCM(ctx, pcm)
	}

	for {
		select {
		case <-ctx.Done():
			terminalErr = ctx.Err()
			return
		case <-disconnected:
			terminalErr = connectionErr()
			if terminalErr == nil {
				terminalErr = errors.New("Baichuan preview connection closed")
			}
			return
		case pcm, ok := <-aacPCM:
			if !ok {
				aacPCM = nil
				continue
			}
			if !emit(pcm, "aac", aacRate) {
				terminalErr = ctx.Err()
				return
			}
		case err, ok := <-aacDone:
			if !ok {
				aacDone = nil
				continue
			}
			aacDone = nil
			if err != nil && !errors.Is(err, context.Canceled) {
				terminalErr = fmt.Errorf("Baichuan AAC decoder: %w", err)
				return
			}
		case packet, ok := <-packets:
			if !ok {
				terminalErr = connectionErr()
				if terminalErr == nil && ctx.Err() == nil {
					terminalErr = errors.New("Baichuan preview ended")
				}
				return
			}
			switch packet.Kind {
			case baichuan.MediaPacketADPCM:
				if activeCodec != "" && activeCodec != "adpcm" {
					terminalErr = fmt.Errorf("Baichuan preview audio codec changed from %s to ADPCM", activeCodec)
					return
				}
				activeCodec = "adpcm"
				cfg.Stats.Received(len(packet.Data))
				pcm := decoder.Decode(packet.Data)
				if len(pcm) == 0 {
					continue
				}
				if adpcmRate != cfg.OutputRate {
					pcm = resampleLinear(pcm, adpcmRate, cfg.OutputRate)
				}
				if !emit(pcm, "adpcm", adpcmRate) {
					terminalErr = ctx.Err()
					return
				}

			case baichuan.MediaPacketAAC:
				if activeCodec != "" && activeCodec != "aac" {
					terminalErr = fmt.Errorf("Baichuan preview audio codec changed from %s to AAC", activeCodec)
					return
				}
				activeCodec = "aac"
				cfg.Stats.Received(len(packet.Data))
				if aac == nil && platformAAC == nil {
					aacRate = adtsSampleRate(packet.Data)
					var err error
					if adapter := platformaudio.Current(); adapter != nil {
						channels := adtsChannelCount(packet.Data)
						if aacRate == 0 || channels == 0 {
							terminalErr = errors.New("Android AAC decoder requires a valid ADTS header")
							return
						}
						platformAAC, err = startPlatformAACDecoder(ctx, adapter, aacRate, channels, cfg.OutputRate)
					} else {
						if strings.TrimSpace(cfg.FFmpegPath) == "" {
							terminalErr = errors.New("FFmpeg binary path is required to decode Baichuan AAC audio")
							return
						}
						aac, err = startAACDecoder(ctx, cfg.FFmpegPath, cfg.OutputRate, cfg.Logger)
					}
					if err != nil {
						terminalErr = err
						return
					}
					if aac != nil {
						aacPCM = aac.PCM()
						aacDone = aac.Done()
					}
				}
				if platformAAC != nil {
					pcm, err := platformAAC.Decode(packet.Data)
					if err != nil {
						terminalErr = fmt.Errorf("decode Baichuan AAC: %w", err)
						return
					}
					if !emit(pcm, "aac", aacRate) {
						terminalErr = ctx.Err()
						return
					}
				} else if err := aac.Write(packet.Data); err != nil {
					terminalErr = fmt.Errorf("feed Baichuan AAC decoder: %w", err)
					return
				}
			}
		}
	}
}

func (r *Receiver) emitPCM(ctx context.Context, pcm []int16) bool {
	if len(pcm) == 0 {
		return true
	}
	copyPCM := append([]int16(nil), pcm...)
	select {
	case r.pcm <- copyPCM:
		return true
	case <-ctx.Done():
		return false
	}
}

// ADTS carries the source sample rate in the fixed header. A zero result means
// the packet did not look like ADTS; FFmpeg is still allowed to inspect it.
func adtsSampleRate(frame []byte) int {
	if len(frame) < 4 || frame[0] != 0xff || frame[1]&0xf6 != 0xf0 {
		return 0
	}
	idx := (frame[2] >> 2) & 0x0f
	rates := [...]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	if int(idx) >= len(rates) {
		return 0
	}
	return rates[idx]
}

func resampleLinear(in []int16, inRate, outRate int) []int16 {
	if len(in) == 0 || inRate <= 0 || outRate <= 0 {
		return nil
	}
	if inRate == outRate {
		return append([]int16(nil), in...)
	}
	outLen := int((int64(len(in))*int64(outRate) + int64(inRate)/2) / int64(inRate))
	if outLen < 1 {
		outLen = 1
	}
	out := make([]int16, outLen)
	if len(in) == 1 {
		for i := range out {
			out[i] = in[0]
		}
		return out
	}
	for i := range out {
		posNum := int64(i) * int64(inRate)
		idx := int(posNum / int64(outRate))
		frac := posNum % int64(outRate)
		if idx >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		a := int64(in[idx])
		b := int64(in[idx+1])
		v := a + (b-a)*frac/int64(outRate)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out
}

type platformAACDecoder struct {
	ctx        context.Context
	adapter    platformaudio.Adapter
	handle     int64
	inputRate  int
	outputRate int
	once       sync.Once
}

func startPlatformAACDecoder(parent context.Context, adapter platformaudio.Adapter, inputRate, channels, outputRate int) (*platformAACDecoder, error) {
	if adapter == nil {
		return nil, errors.New("platform AAC decoder is unavailable")
	}
	if channels != 1 {
		return nil, fmt.Errorf("platform AAC decoder currently supports mono only, got %d channels", channels)
	}
	handle, err := adapter.StartAACDecoder(int32(inputRate), int32(channels))
	if err != nil {
		return nil, fmt.Errorf("start platform AAC decoder: %w", err)
	}
	return &platformAACDecoder{
		ctx: parent, adapter: adapter, handle: handle, inputRate: inputRate, outputRate: outputRate,
	}, nil
}

// Decode returns PCM directly to the receive loop. A buffered PCM channel
// written and drained by that same loop can deadlock as soon as it fills.
func (d *platformAACDecoder) Decode(frame []byte) ([]int16, error) {
	if d == nil || d.adapter == nil {
		return nil, errors.New("platform AAC decoder is not running")
	}
	if err := d.ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := d.adapter.DecodeAAC(d.handle, frame)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw)%2 != 0 {
		return nil, fmt.Errorf("platform AAC decoder returned odd PCM byte count %d", len(raw))
	}
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2 : i*2+2]))
	}
	if d.inputRate != d.outputRate {
		pcm = resampleLinear(pcm, d.inputRate, d.outputRate)
	}
	return pcm, nil
}

func (d *platformAACDecoder) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		d.adapter.StopAACDecoder(d.handle)
	})
}

func adtsChannelCount(frame []byte) int {
	if len(frame) < 4 || frame[0] != 0xff || frame[1]&0xf6 != 0xf0 {
		return 0
	}
	return int((frame[2]&0x01)<<2 | (frame[3]>>6)&0x03)
}

type aacDecoder struct {
	stdin  io.WriteCloser
	pcm    chan []int16
	done   chan error
	cancel context.CancelFunc
	once   sync.Once
}

func startAACDecoder(parent context.Context, ffmpegPath string, outputRate int, logger *slog.Logger) (*aacDecoder, error) {
	if _, err := exec.LookPath(ffmpegPath); err != nil {
		return nil, fmt.Errorf("ffmpeg not found at %q: %w", ffmpegPath, err)
	}
	ctx, cancel := context.WithCancel(parent)
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		// Raw AAC over a never-ending pipe otherwise waits for a large probe
		// window (and can effectively buffer until EOF). The input format is
		// already known, so bound probing to one ADTS-header-sized window and
		// disable duration analysis. This is specific to the local Baichuan AAC
		// decoder and is unrelated to the removed RTSP low-latency option.
		"-probesize", "32", "-analyzeduration", "0",
		"-flags", "low_delay",
		"-f", "aac", "-i", "pipe:0",
		"-map", "0:a:0", "-vn", "-ac", "1", "-ar", fmt.Sprint(outputRate),
		"-c:a", "pcm_s16le", "-f", "s16le", "pipe:1",
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start FFmpeg AAC decoder: %w", err)
	}
	d := &aacDecoder{stdin: stdin, pcm: make(chan []int16, 8), done: make(chan error, 1), cancel: cancel}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), 256*1024)
		for scanner.Scan() {
			if logger != nil {
				logger.Debug("Baichuan AAC decoder", "message", scanner.Text())
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer close(d.pcm)
		buf := make([]byte, outputRate/50*2*4) // up to four 20 ms chunks per read.
		if len(buf) < 640 {
			buf = make([]byte, 640)
		}
		var carry []byte
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				data := make([]byte, 0, len(carry)+n)
				data = append(data, carry...)
				data = append(data, buf[:n]...)
				if len(data)%2 != 0 {
					carry = append(carry[:0], data[len(data)-1])
					data = data[:len(data)-1]
				} else {
					carry = carry[:0]
				}
				if len(data) > 0 {
					pcm := make([]int16, len(data)/2)
					for i := range pcm {
						pcm[i] = int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
					}
					select {
					case d.pcm <- pcm:
					case <-ctx.Done():
						return
					}
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	go func() {
		// StdoutPipe/StderrPipe must be fully drained before Wait is called.
		// Calling Wait concurrently with the readers can close the descriptors
		// early and truncate the final decoded PCM chunk.
		wg.Wait()
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			d.done <- ctx.Err()
		} else if waitErr != nil {
			d.done <- fmt.Errorf("ffmpeg AAC decoder exited: %w", waitErr)
		} else {
			d.done <- nil
		}
		close(d.done)
	}()
	return d, nil
}

func (d *aacDecoder) Write(frame []byte) error {
	if d == nil || d.stdin == nil {
		return errors.New("AAC decoder is not running")
	}
	_, err := d.stdin.Write(frame)
	return err
}

func (d *aacDecoder) PCM() <-chan []int16 { return d.pcm }
func (d *aacDecoder) Done() <-chan error  { return d.done }
func (d *aacDecoder) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() {
		if d.stdin != nil {
			_ = d.stdin.Close()
		}
		d.cancel()
		// Reap the child promptly so repeated door calls cannot accumulate
		// transient FFmpeg processes. The channel is buffered/closed by the
		// waiter goroutine, so this does not depend on a separate consumer.
		select {
		case <-d.done:
		case <-time.After(2 * time.Second):
		}
	})
}
