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
	Run(context.Context, *net.UDPConn, *sip.Call, *audioControls) error
	Close(context.Context) error
	Info() TalkbackInfo
}

type rtspTalkback struct {
	client *rtsp.Client
	bc     rtsp.Backchannel
	logger *slog.Logger
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

func (t *rtspTalkback) Run(ctx context.Context, in *net.UDPConn, call *sip.Call, controls *audioControls) error {
	buf := make([]byte, 4096)
	remote := call.RemoteRTPAddr()
	if remote == nil {
		return errors.New("missing remote RTP address")
	}
	remoteIP := append(net.IP(nil), remote.IP...)
	chunker := &g711Chunker{}
	for {
		_ = in.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, addr, err := in.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
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
		if err != nil || len(p.Payload) == 0 || p.PayloadType != call.Codec.PayloadType {
			continue
		}
		// Retarget symmetric RTP only after validating the negotiated media PT.
		current := call.RemoteRTPAddr()
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
	client  *baichuan.Client
	session *baichuan.TalkSession
	channel int
	logger  *slog.Logger
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

func (t *baichuanTalkback) Run(ctx context.Context, in *net.UDPConn, call *sip.Call, controls *audioControls) error {
	return runBaichuanAudioBridge(ctx, in, call, t.session, t.session.SampleRate(), t.session.SamplesPerBlock(), controls, t.logger, t.client.Done(), t.client.Err)
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
	return &rtspTalkback{client: client, bc: bc, logger: logger}, nil
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
	return &baichuanTalkback{client: client, session: session, channel: cfg.NVRChannel, logger: logger}, nil
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
