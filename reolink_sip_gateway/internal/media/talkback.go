package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
	"github.com/vothmarkus/reolink-sip-gateway/internal/codec"
	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtsp"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
)

type TalkbackInfo struct {
	Mode            string
	Codec           string
	SampleRate      int
	SamplePrecision int
	Channel         int
	Details         string
}

type talkbackTransport interface {
	Run(context.Context, *net.UDPConn, *sip.Call, *audioControls, func(rtp.Packet) bool) error
	PlayPCM(context.Context, []int16, *audioControls) error
	Close(context.Context) error
	Info() TalkbackInfo
}

type rtspTalkback struct {
	client        *rtsp.Client
	bc            rtsp.Backchannel
	logger        *slog.Logger
	rtpInactivity time.Duration
}

func (t *rtspTalkback) Info() TalkbackInfo {
	return TalkbackInfo{
		Mode:       "standalone",
		Codec:      t.bc.Codec,
		SampleRate: 8000,
		Details:    fmt.Sprintf("ONVIF RTSP backchannel, %s/PT%d", strings.ToUpper(t.bc.Codec), t.bc.PayloadType),
	}
}

func (t *rtspTalkback) Close(ctx context.Context) error { return t.client.Shutdown(ctx) }

func (t *rtspTalkback) PlayPCM(ctx context.Context, pcm []int16, controls *audioControls) error {
	const (
		blockSamples  = 160
		blockDuration = 20 * time.Millisecond
	)
	codecName := strings.ToLower(strings.TrimSpace(t.bc.Codec))
	if codecName != g711.PCMA && codecName != g711.PCMU {
		return fmt.Errorf("unsupported RTSP backchannel codec %q for connection tone", t.bc.Codec)
	}
	return playPCMBlocks(ctx, pcm, blockSamples, blockDuration, t.client.Done(), func() error {
		select {
		case err := <-t.client.WaitError():
			return err
		default:
			return nil
		}
	}, func(block []int16) error {
		payload := g711.EncodePCM(block, codecName)
		writeStarted := time.Now()
		if err := t.client.WriteAudio(payload, t.bc.PayloadType); err != nil {
			return err
		}
		if controls != nil && controls.NeedsRenderReference() {
			controls.ObserveG711Playout(payload, codecName, writeStarted)
		}
		return nil
	})
}

func (t *rtspTalkback) Run(ctx context.Context, in *net.UDPConn, call *sip.Call, controls *audioControls, handleTelephoneEvent func(rtp.Packet) bool) error {
	buf := make([]byte, 4096)
	remote := call.RemoteRTPAddr()
	if remote == nil {
		return errors.New("missing remote RTP address")
	}
	remoteIP := append(net.IP(nil), remote.IP...)
	chunker := &g711Chunker{}
	watchdog := newRTPWatchdog(t.rtpInactivity, time.Now())
	for {
		_ = in.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, addr, err := in.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if watchdogErr := watchdog.Check(time.Now()); watchdogErr != nil {
					return watchdogErr
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-t.client.Done():
					select {
					case e := <-t.client.WaitError():
						if e != nil {
							return fmt.Errorf("RTSP connection lost: %w", e)
						}
					default:
					}
					return errors.New("RTSP connection closed")
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
		if err != nil || len(p.Payload) == 0 {
			continue
		}
		current := call.RemoteRTPAddr()
		if handleTelephoneEvent != nil && current != nil && addr.Port == current.Port && handleTelephoneEvent(p) {
			continue
		}
		if p.PayloadType != call.Codec.PayloadType {
			continue
		}
		watchdog.Mark(time.Now())
		// Retarget symmetric RTP only after validating the negotiated media PT.
		if current == nil || addr.Port != current.Port {
			call.UpdateRemoteRTP(addr)
		}
		payload := p.Payload
		if call.Codec.Name != t.bc.Codec {
			payload = g711.ConvertPayload(payload, call.Codec.Name, t.bc.Codec)
		}
		for _, chunk := range chunker.Push(payload) {
			writeStarted := time.Now()
			if err := t.client.WriteAudio(chunk, t.bc.PayloadType); err != nil {
				return err
			}
			if controls != nil && controls.NeedsRenderReference() {
				controls.ObserveG711Playout(chunk, t.bc.Codec, writeStarted)
			}
		}
	}
}

type baichuanTalkback struct {
	client        *baichuan.Client
	session       *baichuan.TalkSession
	channel       int
	logger        *slog.Logger
	rtpInactivity time.Duration
}

func (t *baichuanTalkback) Info() TalkbackInfo {
	return TalkbackInfo{
		Mode:            "nvr",
		Codec:           strings.ToLower(t.session.AudioType()),
		SampleRate:      t.session.SampleRate(),
		SamplePrecision: t.session.SamplePrecision(),
		Channel:         t.channel,
		Details: fmt.Sprintf("Baichuan channel %d, %s %d Hz/%d bit, %s",
			t.channel, strings.ToUpper(t.session.AudioType()), t.session.SampleRate(), t.session.SamplePrecision(), t.session.Duplex()),
	}
}

func (t *baichuanTalkback) Run(ctx context.Context, in *net.UDPConn, call *sip.Call, controls *audioControls, handleTelephoneEvent func(rtp.Packet) bool) error {
	return runBaichuanAudioBridge(ctx, in, call, t.session, t.session.SampleRate(), t.session.SamplesPerBlock(), controls, handleTelephoneEvent, t.logger, t.client.Done(), t.client.Err, t.rtpInactivity)
}

func (t *baichuanTalkback) PlayPCM(ctx context.Context, pcm []int16, controls *audioControls) error {
	rate := t.session.SampleRate()
	blockSamples := t.session.SamplesPerBlock()
	if rate <= 0 || blockSamples < 2 || blockSamples%2 != 0 {
		return errors.New("invalid Baichuan connection-tone profile")
	}
	blockDuration := time.Duration(int64(time.Second) * int64(blockSamples) / int64(rate))
	encoder := &codec.ADPCMEncoder{}
	return playPCMBlocks(ctx, pcm, blockSamples, blockDuration, t.client.Done(), t.client.Err, func(block []int16) error {
		encoded, err := encoder.EncodeBlock(block)
		if err != nil {
			return fmt.Errorf("encode connection tone: %w", err)
		}
		writeStarted := time.Now()
		if err := t.session.WriteADPCMBlock(ctx, encoded); err != nil {
			return err
		}
		if controls != nil && controls.NeedsRenderReference() {
			controls.ObserveBaichuanPlayout(encoded, rate, writeStarted)
		}
		return nil
	})
}

func (t *baichuanTalkback) Close(ctx context.Context) error {
	var errs []error
	if t.session != nil {
		if err := t.session.Close(ctx); err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("stop Baichuan talk: %w", err))
		}
	}
	if t.client != nil {
		if err := t.client.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close Baichuan connection: %w", err))
		}
	}
	return errors.Join(errs...)
}

func openRTSPTalkback(ctx context.Context, cfg config.Config, logger *slog.Logger) (talkbackTransport, error) {
	client := rtsp.New(cfg.RTSPURL(), cfg.ReolinkUsername, cfg.ReolinkPassword, logger, cfg.DebugEnabled())
	bc, err := client.Open(ctx)
	if err != nil {
		return nil, err
	}
	return &rtspTalkback{client: client, bc: bc, logger: logger, rtpInactivity: cfg.RTPInactivityTimeout()}, nil
}

func openBaichuanTalkback(ctx context.Context, cfg config.Config, logger *slog.Logger) (talkbackTransport, error) {
	setupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := baichuan.Dial(setupCtx, baichuan.Config{
		Host: cfg.ReolinkHost, Port: cfg.BaichuanPort,
		Username: cfg.ReolinkUsername, Password: cfg.ReolinkPassword,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("connect Reolink Basic Service: %w", err)
	}
	cleanup := func() { _ = client.Close() }
	session, err := client.StartTalk(setupCtx, uint8(cfg.NVRChannel))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("start Baichuan talk channel %d: %w", cfg.NVRChannel, err)
	}
	if err := validateBaichuanTalkProfile(session.AudioType(), session.SamplePrecision(), session.SampleRate(), session.SamplesPerBlock()); err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = session.Close(closeCtx)
		closeCancel()
		cleanup()
		return nil, err
	}
	if logger != nil {
		logger.Info("Baichuan live talk profile negotiated",
			"channel", cfg.NVRChannel,
			"duplex", session.Duplex(),
			"mode", session.AudioStreamMode(),
			"audio_type", session.AudioType(),
			"sample_rate", session.SampleRate(),
			"sample_precision", session.SamplePrecision(),
			"samples_per_block", session.SamplesPerBlock(),
			"bytes_per_block", session.BytesPerBlock())
	}
	return &baichuanTalkback{client: client, session: session, channel: cfg.NVRChannel, logger: logger, rtpInactivity: cfg.RTPInactivityTimeout()}, nil
}

func validateBaichuanTalkProfile(audioType string, precision, rate, samplesPerBlock int) error {
	if !strings.EqualFold(audioType, "adpcm") {
		return fmt.Errorf("unsupported Baichuan talk codec %q", audioType)
	}
	if precision != 0 && precision != 16 {
		return fmt.Errorf("unsupported Baichuan sample precision %d bit", precision)
	}
	if rate < 8000 || rate > 48000 || samplesPerBlock < 2 || samplesPerBlock%2 != 0 {
		return fmt.Errorf("unsupported Baichuan audio profile: rate=%d samples_per_block=%d", rate, samplesPerBlock)
	}
	blockDuration := time.Duration(int64(time.Second) * int64(samplesPerBlock) / int64(rate))
	if blockDuration < 5*time.Millisecond || blockDuration > 500*time.Millisecond {
		return fmt.Errorf("unsafe Baichuan audio block duration %s (rate=%d samples_per_block=%d)", blockDuration, rate, samplesPerBlock)
	}
	return nil
}

func playPCMBlocks(ctx context.Context, pcm []int16, blockSamples int, blockDuration time.Duration, peerDone <-chan struct{}, peerErr func() error, write func([]int16) error) error {
	if len(pcm) == 0 {
		return nil
	}
	if blockSamples <= 0 || blockDuration <= 0 || write == nil {
		return errors.New("invalid connection-tone block profile")
	}
	next := time.Now()
	blocks := (len(pcm) + blockSamples - 1) / blockSamples
	for blockIndex, offset := 0, 0; blockIndex < blocks; blockIndex, offset = blockIndex+1, offset+blockSamples {
		if blockIndex > 0 {
			next = next.Add(blockDuration)
			if err := waitTalkDeadline(ctx, next, peerDone, peerErr); err != nil {
				return err
			}
		}
		block := make([]int16, blockSamples)
		end := offset + blockSamples
		if end > len(pcm) {
			end = len(pcm)
		}
		copy(block, pcm[offset:end])
		if err := write(block); err != nil {
			return err
		}
	}
	// The final write queues one complete media block. Wait for its playout
	// interval before declaring the indication finished and answering the call.
	return waitTalkDeadline(ctx, next.Add(blockDuration), peerDone, peerErr)
}

func waitTalkDeadline(ctx context.Context, deadline time.Time, peerDone <-chan struct{}, peerErr func() error) error {
	delay := time.Until(deadline)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-peerDone:
		if peerErr != nil {
			if err := peerErr(); err != nil {
				return err
			}
		}
		return errors.New("Reolink talkback connection closed")
	case <-timer.C:
		return nil
	}
}

type talkbackOpener func(context.Context, config.Config, *slog.Logger) (talkbackTransport, error)

func openConfiguredTalkback(ctx context.Context, cfg config.Config, logger *slog.Logger) (talkbackTransport, error) {
	return openConfiguredTalkbackWith(ctx, cfg, logger, openRTSPTalkback, openBaichuanTalkback)
}

func openConfiguredTalkbackWith(ctx context.Context, cfg config.Config, logger *slog.Logger, rtspOpen, baichuanOpen talkbackOpener) (talkbackTransport, error) {
	switch cfg.EffectiveReolinkMode() {
	case "nvr":
		t, err := baichuanOpen(ctx, cfg, logger)
		if err != nil {
			return nil, fmt.Errorf("NVR talkback: %w", err)
		}
		return t, nil
	case "standalone":
		t, err := rtspOpen(ctx, cfg, logger)
		if err != nil {
			return nil, fmt.Errorf("standalone ONVIF talkback: %w", err)
		}
		return t, nil
	case "auto":
		return nil, errors.New("Reolink auto mode was not resolved during startup")
	default:
		return nil, fmt.Errorf("unsupported connection mode %q", cfg.EffectiveReolinkMode())
	}
}
