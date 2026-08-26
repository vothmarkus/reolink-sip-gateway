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
	"sync"
	"syscall"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/callcontrol"
	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/ha"
	"github.com/vothmarkus/reolink-sip-gateway/internal/media"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
	"github.com/vothmarkus/reolink-sip-gateway/internal/startup"
	statuspkg "github.com/vothmarkus/reolink-sip-gateway/internal/status"
)

const version = "1.2.1"

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
	routes := cfg.ResolvedCallRoutes()

	logger := newLogger(cfg.LogLevel)
	logger.Info("starting Reolink SIP Gateway", "version", version, "dry_run", cfg.DryRun)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	identity, err := statuspkg.LoadOrCreateIdentity("/data")
	if err != nil {
		logger.Error("cannot initialize Home Assistant integration API identity", "error", err)
		os.Exit(1)
	}
	commands := &gatewayCommands{}
	addonHostname, hostnameErr := os.Hostname()
	if hostnameErr != nil {
		logger.Warn("cannot determine Home Assistant app hostname", "error", hostnameErr)
	}
	store := statuspkg.New(version)
	store.SetRoutes(statusRouteDefinitions(cfg, routes))
	store.Update(func(s *statuspkg.Snapshot) {
		s.DryRun = cfg.DryRun
		s.DoorCallEnabled = cfg.DoorCallEnabled
		s.ParallelCallEnabled = cfg.ParallelCallEnabled
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
		serverOptions := statuspkg.ServerOptions{
			Port: cfg.StatusPort, Token: identity.Token, InstanceID: identity.InstanceID, Hostname: addonHostname, Commands: commands,
		}
		if err := store.Serve(ctx, serverOptions); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("status server stopped", "error", err)
			cancel()
		}
	}()
	logger.Info("Home Assistant integration API ready", "api_version", statuspkg.APIVersion, "port", cfg.StatusPort, "instance_id", identity.InstanceID)

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

	var doorSIPClient *sip.Client
	var parallelSIPClient *sip.Client
	if cfg.DryRun {
		logger.Info("dry-run enabled; SIP registration, calls and audible startup calibration are disabled")
	} else {
		if cfg.DoorCallEnabled {
			doorSIPClient, err = sip.New(doorSIPConfig(cfg), logger.With("sip_account", "door"))
			if err != nil {
				logger.Error("cannot initialize door SIP account", "error", err)
				os.Exit(1)
			}
			defer doorSIPClient.Close()
		}

		if cfg.ParallelCallEnabled {
			parallelSIPClient, err = sip.New(parallelSIPConfig(cfg), logger.With("sip_account", "mobile"))
			if err != nil {
				if doorSIPClient != nil {
					doorSIPClient.Close()
				}
				logger.Error("cannot initialize mobile SIP account", "error", err)
				os.Exit(1)
			}
			defer parallelSIPClient.Close()
		}

		if doorSIPClient != nil {
			doorSIPClient.StartRegistration(ctx)
			logger.Info("door SIP calling configured", "route_count", countDoorRoutes(routes), "local_port", cfg.SIPLocalPort)
		}
		if parallelSIPClient != nil {
			parallelSIPClient.StartRegistration(ctx)
			logger.Info("mobile SIP parallel calling configured", "route_count", countMobileRoutes(routes), "local_port", cfg.ParallelLocalPort)
		}
		if cfg.IncomingCallsEnabled {
			allowAll := len(cfg.IncomingAllowedCallers) == 1 && cfg.IncomingAllowedCallers[0] == "*"
			logger.Info("incoming SIP call policy active",
				"account_count", configuredSIPAccountCount(cfg),
				"allow_all_callers", allowAll,
				"allowed_caller_count", len(cfg.IncomingAllowedCallers),
				"connection_tone", cfg.IncomingConnectionToneEnabled,
				"rtp_inactivity_timeout", cfg.RTPInactivityTimeout())
		}

		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				store.Update(func(s *statuspkg.Snapshot) {
					if doorSIPClient != nil {
						s.SIPRegistered = doorSIPClient.Registered()
						s.LastRegistrationErr = doorSIPClient.LastRegisterError()
					}
					if parallelSIPClient != nil {
						s.ParallelSIPRegistered = parallelSIPClient.Registered()
						s.LastParallelRegistrationErr = parallelSIPClient.LastRegisterError()
					}
					if s.State == "starting" && anySIPRegistered(doorSIPClient, parallelSIPClient) && s.HAConnected {
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

	triggers := make(chan ha.Trigger, max(1, len(routes)))
	listener := &ha.Listener{
		Token:        token,
		Routes:       routeSubscriptions(routes),
		PollInterval: cfg.HAPollInterval(), // fixed one-second REST fallback; WebSocket remains primary.
		Logger:       logger,
		OnConnection: func(ok bool) {
			store.Update(func(s *statuspkg.Snapshot) {
				s.HAConnected = ok
				if ok && s.State == "starting" && (cfg.DryRun || anySIPRegistered(doorSIPClient, parallelSIPClient)) {
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

	var calls callcontrol.Controller
	commands.Configure(
		func(_ context.Context, routeID string) error {
			route, ok := requestedCallRoute(routes, routeID)
			if !ok {
				return statuspkg.ErrRouteNotFound
			}
			if cfg.DryRun || !routeSIPAvailable(cfg, route, doorSIPClient, parallelSIPClient) {
				return statuspkg.ErrSIPUnavailable
			}
			if err := calls.Start(ctx, func(callCtx context.Context) {
				handleCall(callCtx, cfg, route, doorSIPClient, parallelSIPClient, store, logger)
			}); err != nil {
				if errors.Is(err, callcontrol.ErrBusy) {
					return statuspkg.ErrCallBusy
				}
				return err
			}
			logger.Info("Home Assistant integration test call accepted",
				"route", route.ID,
				"door_leg", cfg.DoorCallEnabled && route.DoorbellNumber != "",
				"mobile_target_count", len(route.MobileTargets))
			return nil
		},
		func(context.Context) error {
			err := calls.CancelActive(func() {
				store.Update(func(s *statuspkg.Snapshot) {
					if s.CurrentCallDirection != "" {
						s.State = "ending"
					}
				})
			})
			if errors.Is(err, callcontrol.ErrNoActiveCall) {
				return statuspkg.ErrNoActiveCall
			}
			if err != nil {
				return err
			}
			logger.Info("Home Assistant integration hangup accepted")
			return nil
		},
	)
	defer commands.Disable()

	lastTriggers := make(map[string]time.Time, len(routes))
	var incomingCalls <-chan *sip.IncomingInvite
	if cfg.IncomingCallsEnabled {
		incomingCalls = mergeIncomingCalls(ctx, doorSIPClient, parallelSIPClient)
	}
	for {
		select {
		case <-ctx.Done():
			store.Update(func(s *statuspkg.Snapshot) { s.State = "stopping" })
			logger.Info("stopping")
			return
		case trigger := <-triggers:
			route, ok := requestedCallRoute(routes, trigger.RouteID)
			if !ok {
				logger.Warn("Home Assistant trigger ignored for unknown route", "route", trigger.RouteID, "entity", trigger.EntityID)
				continue
			}
			now := time.Now()
			if previous := lastTriggers[route.ID]; !previous.IsZero() && now.Sub(previous) < cfg.Debounce() {
				logger.Debug("call-route trigger ignored by debounce", "route", route.ID)
				continue
			}
			lastTriggers[route.ID] = now
			store.Update(func(s *statuspkg.Snapshot) { s.LastVisitorEvent = now })
			if err := calls.Start(ctx, func(callCtx context.Context) {
				handleCall(callCtx, cfg, route, doorSIPClient, parallelSIPClient, store, logger)
			}); err != nil {
				logger.Warn("call-route trigger ignored because a call is active", "route", route.ID)
				continue
			}
		case incoming := <-incomingCalls:
			if incoming == nil {
				continue
			}
			incomingCall := incoming
			if err := calls.Start(ctx, func(callCtx context.Context) {
				handleIncomingCall(callCtx, cfg, incomingCall, store, logger)
			}); err != nil {
				logger.Warn("incoming SIP call rejected because another call is active", "caller", incoming.CallerURI())
				if err := incoming.Reject(486, "Busy Here"); err != nil && !errors.Is(err, sip.ErrIncomingCallCanceled) {
					logger.Debug("cannot reject busy incoming SIP call", "error", err)
				}
				continue
			}
		}
	}
}

func doorSIPConfig(cfg config.Config) sip.Config {
	return sip.Config{
		Registrar:          cfg.SIPRegistrar,
		RegistrarPort:      cfg.SIPRegistrarPort,
		Username:           cfg.SIPUsername,
		Password:           cfg.SIPPassword,
		LocalPort:          cfg.SIPLocalPort,
		DisplayName:        cfg.SIPDisplayName,
		CodecPreference:    cfg.SIPCodecPreference,
		AcceptIncoming:     cfg.IncomingCallsEnabled,
		AllowedCallers:     cfg.IncomingAllowedCallers,
		MaxConcurrentCalls: 1,
		Debug:              cfg.DebugEnabled(),
	}
}

func parallelSIPConfig(cfg config.Config) sip.Config {
	return sip.Config{
		Registrar:          cfg.SIPRegistrar,
		RegistrarPort:      cfg.SIPRegistrarPort,
		Username:           cfg.ParallelUsername,
		Password:           cfg.ParallelPassword,
		LocalPort:          cfg.ParallelLocalPort,
		DisplayName:        "Reolink Mobilruf",
		CodecPreference:    cfg.SIPCodecPreference,
		AcceptIncoming:     cfg.IncomingCallsEnabled,
		AllowedCallers:     cfg.IncomingAllowedCallers,
		MaxConcurrentCalls: cfg.MaxMobileTargetsPerRoute(),
		Debug:              cfg.DebugEnabled(),
	}
}

func routeSubscriptions(routes []config.ResolvedCallRoute) []ha.RouteSubscription {
	result := make([]ha.RouteSubscription, 0, len(routes))
	for _, route := range routes {
		result = append(result, ha.RouteSubscription{RouteID: route.ID, EntityID: route.VisitorEntity})
	}
	return result
}

func statusRouteDefinitions(cfg config.Config, routes []config.ResolvedCallRoute) []statuspkg.RouteDefinition {
	result := make([]statuspkg.RouteDefinition, 0, len(routes))
	for _, route := range routes {
		result = append(result, statuspkg.RouteDefinition{
			ID:         route.ID,
			Name:       route.Name,
			DoorCall:   cfg.DoorCallEnabled && route.DoorbellNumber != "",
			MobileCall: cfg.ParallelCallEnabled && len(route.MobileTargets) > 0,
		})
	}
	return result
}

func requestedCallRoute(routes []config.ResolvedCallRoute, routeID string) (config.ResolvedCallRoute, bool) {
	if routeID == "" {
		if len(routes) == 0 {
			return config.ResolvedCallRoute{}, false
		}
		return routes[0], true
	}
	for _, route := range routes {
		if route.ID == routeID {
			return route, true
		}
	}
	return config.ResolvedCallRoute{}, false
}

func routeSIPAvailable(cfg config.Config, route config.ResolvedCallRoute, doorClient, parallelClient *sip.Client) bool {
	doorAvailable := cfg.DoorCallEnabled && route.DoorbellNumber != "" && doorClient != nil && doorClient.Registered()
	mobileAvailable := cfg.ParallelCallEnabled && len(route.MobileTargets) > 0 && parallelClient != nil && parallelClient.Registered()
	return doorAvailable || mobileAvailable
}

func countDoorRoutes(routes []config.ResolvedCallRoute) int {
	count := 0
	for _, route := range routes {
		if route.DoorbellNumber != "" {
			count++
		}
	}
	return count
}

func countMobileRoutes(routes []config.ResolvedCallRoute) int {
	count := 0
	for _, route := range routes {
		if len(route.MobileTargets) > 0 {
			count++
		}
	}
	return count
}

func configuredSIPAccountCount(cfg config.Config) int {
	count := 0
	if cfg.DoorCallEnabled {
		count++
	}
	if cfg.ParallelCallEnabled {
		count++
	}
	return count
}

func anySIPRegistered(clients ...*sip.Client) bool {
	for _, client := range clients {
		if client != nil && client.Registered() {
			return true
		}
	}
	return false
}

func mergeIncomingCalls(ctx context.Context, clients ...*sip.Client) <-chan *sip.IncomingInvite {
	sources := make([]<-chan *sip.IncomingInvite, 0, len(clients))
	for _, client := range clients {
		if client != nil {
			sources = append(sources, client.IncomingCalls())
		}
	}
	return mergeIncomingInviteChannels(ctx, sources...)
}

func mergeIncomingInviteChannels(ctx context.Context, sources ...<-chan *sip.IncomingInvite) <-chan *sip.IncomingInvite {
	if len(sources) == 0 {
		return nil
	}
	merged := make(chan *sip.IncomingInvite, len(sources))
	for _, source := range sources {
		source := source
		go func() {
			for {
				select {
				case invite, ok := <-source:
					if !ok {
						return
					}
					select {
					case merged <- invite:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	return merged
}

func handleIncomingCall(parent context.Context, cfg config.Config, incoming *sip.IncomingInvite, store *statuspkg.Store, logger *slog.Logger) {
	started := time.Now()
	call := incoming.Call()
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "connecting_media"
		s.LastCallStarted = started
		s.CurrentCallDirection = "incoming"
		s.LastCallDirection = "incoming"
		s.CurrentCallerNumber = incoming.CallerID()
		s.CurrentRouteID = ""
		s.CurrentRouteName = ""
		if incoming.CallerID() != "" {
			s.LastCallerNumber = incoming.CallerID()
		}
		s.LastError = ""
		s.ActiveCodec = call.Codec.Name
		s.ActiveTalkback = ""
		s.TalkbackDetails = ""
		s.ActiveReceive = ""
		s.ReceiveDetails = ""
		s.ActiveEchoCancellation = ""
		s.CurrentDelayMS = cfg.AECInitialDelayMS
	})

	rejectUnavailable := func(cause error) {
		if err := incoming.Reject(480, "Temporarily Unavailable"); err != nil && !errors.Is(err, sip.ErrIncomingCallCanceled) {
			logger.Debug("cannot reject unavailable incoming SIP call", "error", err)
		}
		recordIncomingCallError(store, logger, cause)
	}

	rtpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: call.ClientLocalIP(), Port: 0})
	if err != nil {
		rejectUnavailable(fmt.Errorf("reserve SIP RTP port: %w", err))
		return
	}
	defer rtpConn.Close()
	rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port

	var ffConn *net.UDPConn
	if cfg.ReceiveMode() == "rtsp" {
		ffConn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		if err != nil {
			rejectUnavailable(fmt.Errorf("reserve local FFmpeg RTP port: %w", err))
			return
		}
		defer ffConn.Close()
	}
	logger.Debug("dynamic media ports reserved for incoming call", "sip_rtp_port", rtpPort, "rtsp_receive", cfg.ReceiveMode() == "rtsp")

	callCtx, cancelCall := context.WithTimeout(parent, cfg.MaxCallDuration())
	defer cancelCall()
	mediaSession := media.New(cfg, call, rtpConn, ffConn, logger)
	mediaErr := make(chan error, 1)
	go func() { mediaErr <- mediaSession.Run(callCtx) }()
	go forwardMediaEvents(callCtx, mediaSession, store, "incoming", incoming.CallerID(), call.CallID)

	var ready media.SessionInfo
	select {
	case ready = <-mediaSession.Ready():
		if err := incoming.Answer(rtpPort); err != nil {
			cancelCall()
			_ = waitMedia(mediaErr, 5*time.Second)
			if errors.Is(err, sip.ErrIncomingCallCanceled) {
				finishCanceledIncomingCall(store, logger, started)
			} else {
				recordIncomingCallError(store, logger, fmt.Errorf("answer incoming SIP call: %w", err))
			}
			return
		}
		store.Update(func(s *statuspkg.Snapshot) {
			s.State = "active"
			s.ActiveTalkback = ready.Talkback.Mode
			s.TalkbackDetails = ready.Talkback.Details
			s.ActiveReceive = ready.Receive.Mode
			s.ReceiveDetails = ready.Receive.Details
			s.ActiveEchoCancellation = ready.EchoCancellation
		})
		logger.Info("incoming call media active", "caller", incoming.CallerURI(), "sip_codec", call.Codec.Name, "receive_mode", ready.Receive.Mode, "receive", ready.Receive.Details, "talkback_mode", ready.Talkback.Mode, "talkback", ready.Talkback.Details, "echo_cancellation", ready.EchoCancellation)
	case err := <-mediaErr:
		cancelCall()
		rejectUnavailable(fmt.Errorf("prepare Reolink media for incoming call: %w", err))
		return
	case err := <-call.Done():
		cancelCall()
		_ = waitMedia(mediaErr, 5*time.Second)
		if errors.Is(err, sip.ErrIncomingCallCanceled) || err == nil {
			finishCanceledIncomingCall(store, logger, started)
		} else {
			recordIncomingCallError(store, logger, err)
		}
		return
	case <-callCtx.Done():
		cancelCall()
		_ = waitMedia(mediaErr, 5*time.Second)
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			rejectUnavailable(errors.New("incoming call media preparation timed out"))
		} else {
			if err := incoming.Reject(480, "Temporarily Unavailable"); err != nil && !errors.Is(err, sip.ErrIncomingCallCanceled) {
				logger.Debug("cannot reject canceled incoming SIP call", "error", err)
			}
			finishCall(store, logger, started, nil, "incoming call ended")
		}
		return
	}

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

	finishCall(store, logger, started, finalErr, "incoming call ended")
}

func handleCall(parent context.Context, cfg config.Config, route config.ResolvedCallRoute, doorClient, parallelClient *sip.Client, store *statuspkg.Store, logger *slog.Logger) {
	logger = logger.With("route", route.ID)
	started := time.Now()
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "dialing"
		s.LastCallStarted = started
		s.CurrentCallDirection = "outgoing"
		s.LastCallDirection = "outgoing"
		s.CurrentCallerNumber = ""
		s.CurrentRouteID = route.ID
		s.CurrentRouteName = route.Name
		s.LastRouteID = route.ID
		s.LastRouteName = route.Name
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
		logger.Info("dry-run call-route event received; SIP call suppressed")
		store.Update(func(s *statuspkg.Snapshot) {
			s.State = "idle"
			s.LastCallEnded = time.Now()
			clearActiveCall(s)
		})
		return
	}
	legs := outboundDialLegs(cfg, route, doorClient, parallelClient, logger)
	if len(legs) == 0 {
		recordCallError(store, logger, errors.New("no configured SIP account is registered"))
		return
	}
	ringCtx, cancelRing := context.WithTimeout(parent, cfg.RingTimeout())
	winner, err := callcontrol.DialFirst(ringCtx, legs)
	cancelRing()
	if err != nil {
		waitForkCleanup(winner.LosersDone, logger)
		if errors.Is(err, context.Canceled) && parent.Err() != nil {
			finishCall(store, logger, started, nil, "call canceled")
			return
		}
		recordCallError(store, logger, fmt.Errorf("all SIP call legs failed: %w", err))
		return
	}
	candidate, ok := winner.Dialog.(*outboundCandidate)
	if !ok || candidate.call == nil || candidate.rtpConn == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = winner.Dialog.Hangup(ctx)
		cancel()
		waitForkCleanup(winner.LosersDone, logger)
		recordCallError(store, logger, errors.New("SIP fork returned an invalid winning dialog"))
		return
	}
	defer candidate.Close()
	call := candidate.call
	rtpConn := candidate.rtpConn
	logger.Info("SIP fork winner selected", "leg", winner.Leg.ID, "codec", call.Codec.Name)

	var ffConn *net.UDPConn
	if cfg.ReceiveMode() == "rtsp" {
		ffConn, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		if err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = call.Hangup(ctx)
			cancel()
			waitForkCleanup(winner.LosersDone, logger)
			recordCallError(store, logger, fmt.Errorf("reserve local FFmpeg RTP port: %w", err))
			return
		}
		defer ffConn.Close()
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
	go forwardMediaEvents(
		callCtx,
		mediaSession,
		store,
		"outgoing",
		sip.CanonicalRemoteNumber(winner.Leg.Destination),
		call.CallID,
	)
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

	waitForkCleanup(winner.LosersDone, logger)
	finishCall(store, logger, started, finalErr, "call ended")
}

type outboundCandidate struct {
	call      *sip.Call
	rtpConn   *net.UDPConn
	closeOnce sync.Once
}

func (c *outboundCandidate) Hangup(ctx context.Context) error {
	err := c.call.Hangup(ctx)
	c.Close()
	return err
}

func (c *outboundCandidate) Close() {
	c.closeOnce.Do(func() {
		_ = c.rtpConn.Close()
	})
}

func outboundDialLegs(cfg config.Config, route config.ResolvedCallRoute, doorClient, parallelClient *sip.Client, logger *slog.Logger) []callcontrol.DialLeg {
	legs := make([]callcontrol.DialLeg, 0, 1+len(route.MobileTargets))
	if cfg.DoorCallEnabled && route.DoorbellNumber != "" {
		if doorClient != nil && doorClient.Registered() {
			legs = append(legs, newOutboundDialLeg("door", route.DoorbellNumber, doorClient, logger))
		} else if doorClient != nil {
			logger.Warn("door SIP call leg skipped because its account is not registered")
		}
	}
	if !cfg.ParallelCallEnabled {
		return legs
	}
	if parallelClient == nil || !parallelClient.Registered() {
		logger.Warn("mobile SIP call legs skipped because the mobile account is not registered", "target_count", len(route.MobileTargets))
		return legs
	}
	for _, target := range route.MobileTargets {
		id := "mobile_" + target.ID
		legs = append(legs, newOutboundDialLeg(id, target.Destination, parallelClient, logger))
	}
	return legs
}

func newOutboundDialLeg(id, destination string, client *sip.Client, logger *slog.Logger) callcontrol.DialLeg {
	return callcontrol.DialLeg{
		ID: id, Destination: destination,
		Dial: func(ctx context.Context) (callcontrol.Dialog, error) {
			rtpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: client.LocalIP(), Port: 0})
			if err != nil {
				return nil, fmt.Errorf("reserve SIP RTP port: %w", err)
			}
			rtpPort := rtpConn.LocalAddr().(*net.UDPAddr).Port
			logger.Debug("SIP fork media port reserved", "leg", id, "sip_rtp_port", rtpPort)
			call, err := client.Dial(ctx, destination, rtpPort)
			if err != nil {
				_ = rtpConn.Close()
				return nil, err
			}
			return &outboundCandidate{call: call, rtpConn: rtpConn}, nil
		},
	}
}

func waitForkCleanup(done <-chan struct{}, logger *slog.Logger) {
	if done == nil {
		return
	}
	timer := time.NewTimer(7 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		logger.Warn("SIP fork loser cleanup did not finish before timeout")
	}
}

func forwardMediaEvents(
	ctx context.Context,
	session *media.Session,
	store *statuspkg.Store,
	callDirection, remoteNumber, callID string,
) {
	for {
		select {
		case update := <-session.AECStatus():
			store.Update(func(s *statuspkg.Snapshot) { s.CurrentDelayMS = update.CurrentDelayMS })
		case event := <-session.DTMFEvents():
			store.PublishDTMF(
				event.Digit,
				event.DurationMS,
				event.ReceivedAt,
				callDirection,
				remoteNumber,
				callID,
			)
		case <-ctx.Done():
			return
		}
	}
}

func finishCall(store *statuspkg.Store, logger *slog.Logger, started time.Time, finalErr error, message string) {
	ended := time.Now()
	if finalErr != nil && !errors.Is(finalErr, context.Canceled) {
		logger.Warn(message+" with error", "error", finalErr)
		store.Update(func(s *statuspkg.Snapshot) {
			s.State = "error"
			s.LastError = finalErr.Error()
			s.LastCallEnded = ended
			clearActiveCall(s)
		})
		return
	}
	logger.Info(message, "duration", ended.Sub(started).Round(time.Second))
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "idle"
		s.LastError = ""
		s.LastCallEnded = ended
		clearActiveCall(s)
	})
}

func finishCanceledIncomingCall(store *statuspkg.Store, logger *slog.Logger, started time.Time) {
	logger.Info("incoming SIP call canceled before answer", "duration", time.Since(started).Round(time.Second))
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "idle"
		s.LastError = ""
		s.LastCallEnded = time.Now()
		clearActiveCall(s)
	})
}

func recordIncomingCallError(store *statuspkg.Store, logger *slog.Logger, err error) {
	logger.Error("cannot accept incoming call", "error", err)
	store.Update(func(s *statuspkg.Snapshot) {
		s.State = "error"
		s.LastError = err.Error()
		s.LastCallEnded = time.Now()
		clearActiveCall(s)
	})
}

func clearActiveCall(s *statuspkg.Snapshot) {
	s.CurrentCallDirection = ""
	s.CurrentCallerNumber = ""
	s.CurrentRouteID = ""
	s.CurrentRouteName = ""
	clearActiveMedia(s)
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
		clearActiveCall(s)
	})
}

// gatewayCommands lets the status/API server start before acoustic startup
// preparation has completed. API calls fail closed until Configure installs
// callbacks that capture the final runtime configuration and SIP client.
type gatewayCommands struct {
	mu       sync.RWMutex
	testCall func(context.Context, string) error
	hangup   func(context.Context) error
}

func (c *gatewayCommands) Configure(testCall func(context.Context, string) error, hangup func(context.Context) error) {
	c.mu.Lock()
	c.testCall = testCall
	c.hangup = hangup
	c.mu.Unlock()
}

func (c *gatewayCommands) Disable() {
	c.Configure(nil, nil)
}

func (c *gatewayCommands) StartTestCall(ctx context.Context, routeID string) error {
	c.mu.RLock()
	command := c.testCall
	c.mu.RUnlock()
	if command == nil {
		return statuspkg.ErrCommandUnavailable
	}
	return command(ctx, routeID)
}

func (c *gatewayCommands) Hangup(ctx context.Context) error {
	c.mu.RLock()
	command := c.hangup
	c.mu.RUnlock()
	if command == nil {
		return statuspkg.ErrCommandUnavailable
	}
	return command(ctx)
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
