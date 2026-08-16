package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/ha"
	"github.com/vothmarkus/reolink-sip-gateway/internal/media"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
	"github.com/vothmarkus/reolink-sip-gateway/internal/startup"
	statuspkg "github.com/vothmarkus/reolink-sip-gateway/internal/status"
)

const version = "0.5.14"

func main() {
	configPath := flag.String("config", "/data/options.json", "path to Home Assistant app options JSON")
	checkOnly := flag.Bool("check-config", false, "validate configuration and exit")
	resolveVisitor := flag.Bool("resolve-visitor-entity", false, "resolve the enabled Reolink visitor binary sensor from the Home Assistant entity registry and exit")
	flag.Parse()

	if *resolveVisitor {
		token := os.Getenv("SUPERVISOR_TOKEN")
		if token == "" {
			fmt.Fprintln(os.Stderr, "visitor entity resolution error: SUPERVISOR_TOKEN is missing; homeassistant_api must be enabled")
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		entityID, err := ha.ResolveReolinkVisitorEntity(ctx, "", token)
		if err != nil {
			fmt.Fprintln(os.Stderr, "visitor entity resolution error:", err)
			os.Exit(2)
		}
		fmt.Println(entityID)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(2)
	}
	if *checkOnly {
		fmt.Println("configuration valid")
		return
	}

	logger := newLogger(cfg.LogLevel)
	logger.Info("starting Reolink SIP Gateway", "version", version, "dry_run", cfg.DryRun)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store := statuspkg.New(version)
	store.Update(func(s *statuspkg.Snapshot) {
		s.ConfiguredReolinkMode = cfg.ReolinkMode
		s.EchoCancellationEnabled = cfg.EchoCancellationEnabled
		s.CalibratedDelayMS = cfg.AECInitialDelayMS
		s.CurrentDelayMS = cfg.AECInitialDelayMS
		s.AECSearchWindowMS = cfg.EchoCancellationSearchWindowMS
		s.AECMinDelayMS = cfg.AECMinDelayMS
		s.AECMaxDelayMS = cfg.AECMaxDelayMS
		s.WebRTCHighPassFilterEnabled = cfg.WebRTCHighPassFilterEnabled
		s.WebRTCNoiseSuppressionEnabled = cfg.WebRTCNoiseSuppressionEnabled
		if cfg.EchoCancellationEnabled {
			s.CalibrationStatus = "pending"
		} else {
			s.CalibrationStatus = "AEC disabled"
		}
	})
	go func() {
		if err := store.Serve(ctx, cfg.StatusPort); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("status server stopped", "error", err)
			cancel()
		}
	}()

	token := os.Getenv("SUPERVISOR_TOKEN")
	if token == "" {
		store.Update(func(s *statuspkg.Snapshot) {
			s.State = "error"
			s.LastError = "SUPERVISOR_TOKEN is missing; homeassistant_api must be enabled"
		})
		logger.Error("SUPERVISOR_TOKEN is missing; homeassistant_api must be enabled")
		os.Exit(1)
	}

	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "preparing"
		if cfg.EchoCancellationEnabled && !cfg.DryRun {
			s.CalibrationStatus = "measuring"
			s.CalibrationDetails = "automatic startup calibration in progress"
		}
	})
	prepared, err := startup.Prepare(ctx, cfg, logger)
	if err != nil {
		store.Update(func(s *statuspkg.Snapshot) {
			s.State = "error"
			s.LastError = err.Error()
			s.CalibrationStatus = "failed"
		})
		logger.Error("Reolink startup preparation failed", "error", err)
		os.Exit(1)
	}
	cfg = prepared.Config
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "starting"
		s.ActiveReolinkMode = prepared.ActiveMode
		s.MediaProfile = prepared.MediaProfile
		s.CalibrationStatus = prepared.CalibrationStatus
		s.CalibrationDetails = prepared.CalibrationDetails
		s.LastCalibration = prepared.LastCalibration
		s.CalibratedDelayMS = cfg.AECInitialDelayMS
		s.CurrentDelayMS = cfg.AECInitialDelayMS
		s.AECMinDelayMS = cfg.AECMinDelayMS
		s.AECMaxDelayMS = cfg.AECMaxDelayMS
	})
	if prepared.ActiveMode != "" {
		logger.Info("Reolink media profile ready",
			"configured_mode", cfg.ReolinkMode,
			"active_mode", prepared.ActiveMode,
			"media_profile", prepared.MediaProfile)
	}

	var sipClient *sip.Client
	if cfg.DryRun {
		logger.Info("dry-run enabled; SIP registration, calls and audible startup calibration are disabled")
	} else {
		sipClient, err = sip.New(sip.Config{
			Registrar:       cfg.SIPRegistrar,
			RegistrarPort:   cfg.SIPRegistrarPort,
			Username:        cfg.SIPUsername,
			Password:        cfg.SIPPassword,
			LocalPort:       cfg.SIPLocalPort,
			DisplayName:     cfg.SIPDisplayName,
			CodecPreference: cfg.SIPCodecPreference,
			Debug:           cfg.DebugEnabled(),
		}, logger)
		if err != nil {
			logger.Error("cannot initialize SIP", "error", err)
			os.Exit(1)
		}
		defer sipClient.Close()
		sipClient.StartRegistration(ctx)

		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				store.Update(func(s *statuspkg.Snapshot) {
					s.SIPRegistered = sipClient.Registered()
					s.LastRegistrationErr = sipClient.LastRegisterError()
					if s.State == "starting" && sipClient.Registered() && s.HAConnected {
						s.State = "idle"
					}
				})
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}

	triggers := make(chan struct{}, 1)
	listener := &ha.Listener{
		Token:        token,
		EntityID:     cfg.VisitorEntity,
		PollInterval: cfg.HAPollInterval(), // fixed one-second REST fallback; WebSocket remains primary.
		Logger:       logger,
		OnConnection: func(ok bool) {
			store.Update(func(s *statuspkg.Snapshot) {
				s.HAConnected = ok
				if ok && s.State == "starting" && (cfg.DryRun || (sipClient != nil && sipClient.Registered())) {
					s.State = "idle"
				}
			})
		},
	}
	go func() {
		if err := listener.Run(ctx, triggers); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("Home Assistant listener stopped", "error", err)
			cancel()
		}
	}()

	var callActive atomic.Bool
	var lastTrigger atomic.Int64
	for {
		select {
		case <-ctx.Done():
			store.Update(func(s *statuspkg.Snapshot) { s.State = "stopping" })
			logger.Info("stopping")
			return
		case <-triggers:
			now := time.Now()
			prevNanos := lastTrigger.Load()
			if prevNanos != 0 && now.Sub(time.Unix(0, prevNanos)) < cfg.Debounce() {
				logger.Debug("visitor trigger ignored by debounce")
				continue
			}
			lastTrigger.Store(now.UnixNano())
			store.Update(func(s *statuspkg.Snapshot) { s.LastVisitorEvent = now })
			if !callActive.CompareAndSwap(false, true) {
				logger.Warn("visitor trigger ignored because a call is active")
				continue
			}
			go func() {
				defer callActive.Store(false)
				handleCall(ctx, cfg, sipClient, store, logger)
			}()
		}
	}
}

func handleCall(parent context.Context, cfg config.Config, sipClient *sip.Client, store *statuspkg.Store, logger *slog.Logger) {
	started := time.Now()
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "dialing"
		s.LastCallStarted = started
		s.LastError = ""
		s.ActiveCodec = ""
		s.ActiveTalkback = ""
		s.TalkbackDetails = ""
		s.ActiveReceive = ""
		s.ReceiveDetails = ""
		s.ActiveEchoCancellation = ""
		s.CurrentDelayMS = cfg.AECInitialDelayMS
	})
	if cfg.DryRun {
		logger.Info("dry-run visitor event received; SIP call suppressed")
		store.Update(func(s *statuspkg.Snapshot) {
			s.State = "idle"
			s.LastCallEnded = time.Now()
		})
		return
	}
	if sipClient == nil {
		recordCallError(store, logger, errors.New("SIP client is not initialized"))
		return
	}
	if !sipClient.Registered() {
		recordCallError(store, logger, errors.New("SIP is not registered"))
		return
	}

	// Let the kernel select a free RTP port. The selected port is known before
	// INVITE and is advertised in SDP, eliminating a user-facing port setting and
	// avoiding collisions with other local media sessions.
	rtpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: sipClient.LocalIP(), Port: 0})
	if err != nil {
		recordCallError(store, logger, fmt.Errorf("reserve SIP RTP port: %w", err))
		return
	}
	defer rtpConn.Close()
	rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port

	var ffConn *net.UDPConn
	if cfg.ReceiveMode() == "rtsp" {
		ffConn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		if err != nil {
			recordCallError(store, logger, fmt.Errorf("reserve local FFmpeg RTP port: %w", err))
			return
		}
		defer ffConn.Close()
	}
	logger.Debug("dynamic media ports reserved", "sip_rtp_port", rtpPort, "rtsp_receive", cfg.ReceiveMode() == "rtsp")

	ringCtx, cancelRing := context.WithTimeout(parent, cfg.RingTimeout())
	call, err := sipClient.Dial(ringCtx, cfg.SIPDestination, rtpPort)
	cancelRing()
	if err != nil {
		recordCallError(store, logger, fmt.Errorf("SIP call failed: %w", err))
		return
	}
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "connecting_media"
		s.ActiveCodec = call.Codec.Name
	})

	callCtx, cancelCall := context.WithTimeout(parent, cfg.MaxCallDuration())
	defer cancelCall()
	mediaSession := media.New(cfg, call, rtpConn, ffConn, logger)
	mediaErr := make(chan error, 1)
	go func() { mediaErr <- mediaSession.Run(callCtx) }()
	go func() {
		for {
			select {
			case update := <-mediaSession.AECStatus():
				store.Update(func(s *statuspkg.Snapshot) { s.CurrentDelayMS = update.CurrentDelayMS })
			case <-callCtx.Done():
				return
			}
		}
	}()
	go func() {
		select {
		case info := <-mediaSession.Ready():
			if callCtx.Err() != nil {
				return
			}
			store.Update(func(s *statuspkg.Snapshot) {
				if s.State == "connecting_media" {
					s.State = "active"
				}
				s.ActiveTalkback = info.Talkback.Mode
				s.TalkbackDetails = info.Talkback.Details
				s.ActiveReceive = info.Receive.Mode
				s.ReceiveDetails = info.Receive.Details
				s.ActiveEchoCancellation = info.EchoCancellation
			})
			logger.Info("call media active", "sip_codec", call.Codec.Name, "receive_mode", info.Receive.Mode, "receive", info.Receive.Details, "talkback_mode", info.Talkback.Mode, "talkback", info.Talkback.Details, "echo_cancellation", info.EchoCancellation)
		case <-callCtx.Done():
		}
	}()

	var finalErr error
	select {
	case err := <-call.Done():
		finalErr = err
		cancelCall()
		if mediaStopErr := waitMedia(mediaErr, 5*time.Second); finalErr == nil && mediaStopErr != nil {
			finalErr = mediaStopErr
		}
	case err := <-mediaErr:
		finalErr = err
		if errors.Is(err, context.DeadlineExceeded) && errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			finalErr = errors.New("maximum call duration reached")
		}
		cancelCall()
		hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		hangupErr := call.Hangup(hctx)
		cancel()
		if finalErr == nil && hangupErr != nil {
			finalErr = hangupErr
		}
	case <-callCtx.Done():
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			finalErr = errors.New("maximum call duration reached")
		}
		hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		hangupErr := call.Hangup(hctx)
		cancel()
		if finalErr == nil && hangupErr != nil && parent.Err() == nil {
			finalErr = hangupErr
		}
		if mediaStopErr := waitMedia(mediaErr, 5*time.Second); finalErr == nil && mediaStopErr != nil {
			finalErr = mediaStopErr
		}
	}

	ended := time.Now()
	if finalErr != nil && !errors.Is(finalErr, context.Canceled) {
		logger.Warn("call ended with error", "error", finalErr)
		store.Update(func(s *statuspkg.Snapshot) {
			s.State = "error"
			s.LastError = finalErr.Error()
			s.LastCallEnded = ended
			clearActiveMedia(s)
		})
	} else {
		logger.Info("call ended", "duration", ended.Sub(started).Round(time.Second))
		store.Update(func(s *statuspkg.Snapshot) {
			s.State = "idle"
			s.LastCallEnded = ended
			clearActiveMedia(s)
		})
	}
}

func clearActiveMedia(s *statuspkg.Snapshot) {
	s.ActiveCodec = ""
	s.ActiveTalkback = ""
	s.TalkbackDetails = ""
	s.ActiveReceive = ""
	s.ReceiveDetails = ""
	s.ActiveEchoCancellation = ""
}

func waitMedia(ch <-chan error, timeout time.Duration) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-ch:
		if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-t.C:
		return errors.New("media shutdown timed out")
	}
}

func recordCallError(store *statuspkg.Store, logger *slog.Logger, err error) {
	logger.Error("cannot place call", "error", err)
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "error"
		s.LastError = err.Error()
		s.LastCallEnded = time.Now()
		clearActiveMedia(s)
	})
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
