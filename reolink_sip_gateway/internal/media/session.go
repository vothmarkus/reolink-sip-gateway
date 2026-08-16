package media

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuan"
	"github.com/vothmarkus/reolink-sip-gateway/internal/baichuanaudio"
	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
)

var rtspUserInfoPattern = regexp.MustCompile(`(?i)rtsp://[^\s/@]+(?::[^\s@]*)?@`)

type ReceiveInfo struct {
	Mode    string
	Details string
}

type SessionInfo struct {
	Talkback         TalkbackInfo
	Receive          ReceiveInfo
	EchoCancellation string
}

type AECStatus struct {
	CurrentDelayMS int
}

type Session struct {
	cfg                     config.Config
	call                    *sip.Call
	rtpConn                 *net.UDPConn
	ffConn                  *net.UDPConn
	logger                  *slog.Logger
	ready                   chan SessionInfo
	readyOnce               sync.Once
	aecStatus               chan AECStatus
	ffmpegTimestampWarnings atomic.Uint64
}

// New takes ownership of the SIP RTP socket and, in RTSP receive mode, the
// local FFmpeg RTP socket for the lifetime of Run. The required sockets are
// intentionally opened before the SIP INVITE so local media-port conflicts
// cannot surface only after the callee has already answered.
func New(cfg config.Config, call *sip.Call, rtpConn, ffConn *net.UDPConn, logger *slog.Logger) *Session {
	return &Session{cfg: cfg, call: call, rtpConn: rtpConn, ffConn: ffConn, logger: logger, ready: make(chan SessionInfo, 1), aecStatus: make(chan AECStatus, 1)}
}

// Ready receives exactly once after talkback negotiation succeeded and FFmpeg
// was started. It deliberately means "media workers are active", not that the
// first camera RTP packet has already arrived.
func (s *Session) Ready() <-chan SessionInfo   { return s.ready }
func (s *Session) AECStatus() <-chan AECStatus { return s.aecStatus }

func (s *Session) signalReady(info SessionInfo) {
	s.readyOnce.Do(func() { s.ready <- info })
}

func (s *Session) Run(ctx context.Context) error {
	if s.rtpConn == nil {
		return errors.New("SIP RTP socket is not prepared")
	}
	if s.cfg.ReceiveMode() == "rtsp" && s.ffConn == nil {
		return errors.New("ffmpeg RTP socket is not prepared")
	}
	if _, err := exec.LookPath(s.cfg.FFmpegPath()); err != nil {
		return fmt.Errorf("ffmpeg not found at %q: %w", s.cfg.FFmpegPath(), err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	controls := newAudioControls()
	var echo *echoCanceller
	if s.cfg.EchoCancellationEnabled {
		var echoErr error
		echo, echoErr = startEchoCanceller(runCtx, s.cfg, s.logger)
		if echoErr != nil {
			return fmt.Errorf("start WebRTC echo cancellation: %w", echoErr)
		}
		controls.SetRenderObserver(echo.AddRender)
		echo.SetStatusCallback(func(st echoStats) {
			update := AECStatus{CurrentDelayMS: st.CurrentDelayMS}
			select {
			case s.aecStatus <- update:
			default:
				select {
				case <-s.aecStatus:
				default:
				}
				select {
				case s.aecStatus <- update:
				default:
				}
			}
		})
		defer echo.Close()
	}
	if s.logger != nil {
		s.logger.Info("audio processing configuration",
			"receive_mode", s.cfg.ReceiveMode(),
			"echo_cancellation_enabled", s.cfg.EchoCancellationEnabled,
			"echo_delay_ms", s.cfg.AECInitialDelayMS,
			"echo_delay_tracking", productionAECDelayTracking,
			"webrtc_high_pass_filter", s.cfg.WebRTCHighPassFilterEnabled,
			"webrtc_noise_suppression", s.cfg.WebRTCNoiseSuppressionEnabled,
			"webrtc_noise_suppression_level", config.WebRTCNoiseSuppressionLevel)
	}
	defer s.rtpConn.Close()
	if s.ffConn != nil {
		defer s.ffConn.Close()
	}

	talkback, err := openConfiguredTalkback(runCtx, s.cfg, s.logger)
	if err != nil {
		return fmt.Errorf("open Reolink talkback: %w", err)
	}
	talkInfo := talkback.Info()
	if s.logger != nil {
		s.logger.Info("Reolink talkback active", "mode", talkInfo.Mode, "details", talkInfo.Details)
	}

	var (
		ffCmd       *exec.Cmd
		ffErr       <-chan error
		bcReceiver  *baichuanaudio.Receiver
		receiveInfo ReceiveInfo
	)
	cleanupReceive := func() {
		if bcReceiver != nil {
			bcReceiver.Close()
		}
		if ffCmd != nil && ffCmd.Process != nil {
			_ = ffCmd.Process.Kill()
		}
	}

	switch s.cfg.ReceiveMode() {
	case "rtsp":
		ffmpegPort := s.ffConn.LocalAddr().(*net.UDPAddr).Port
		ffCmd, ffErr, err = s.startFFmpeg(runCtx, ffmpegPort, s.call.Codec)
		if err != nil {
			s.closeTalkback(talkback)
			return err
		}
		receiveInfo = ReceiveInfo{Mode: "rtsp", Details: fmt.Sprintf("RTSP %s", s.cfg.ReolinkStreamPath)}
	case "baichuan":
		stream, parseErr := baichuan.ParseStream(config.BaichuanReceiveStream)
		if parseErr != nil {
			s.closeTalkback(talkback)
			return parseErr
		}
		bcReceiver, err = baichuanaudio.Open(runCtx, baichuanaudio.Config{
			Host: s.cfg.ReolinkHost, Port: s.cfg.BaichuanPort,
			Username: s.cfg.ReolinkUsername, Password: s.cfg.ReolinkPassword,
			Channel: uint8(s.cfg.NVRChannel), Stream: stream,
			OutputRate: g711SampleRate, FFmpegPath: s.cfg.FFmpegPath(),
			Logger: s.logger, Debug: s.cfg.DebugEnabled(),
		})
		if err != nil {
			s.closeTalkback(talkback)
			return fmt.Errorf("open Baichuan camera receive: %w", err)
		}
		readyTimer := time.NewTimer(10 * time.Second)
		select {
		case ri, ok := <-bcReceiver.Ready():
			if !readyTimer.Stop() {
				<-readyTimer.C
			}
			if !ok {
				cleanupReceive()
				s.closeTalkback(talkback)
				return errors.New("Baichuan receive ended before audio became ready")
			}
			receiveInfo = ReceiveInfo{Mode: "baichuan", Details: ri.Details()}
		case recvErr := <-bcReceiver.Done():
			if !readyTimer.Stop() {
				<-readyTimer.C
			}
			cleanupReceive()
			s.closeTalkback(talkback)
			if recvErr == nil {
				recvErr = errors.New("Baichuan receive ended before audio became ready")
			}
			return fmt.Errorf("Baichuan camera receive: %w", recvErr)
		case <-readyTimer.C:
			cleanupReceive()
			s.closeTalkback(talkback)
			return errors.New("no Baichuan camera audio detected within 10 seconds")
		case <-runCtx.Done():
			if !readyTimer.Stop() {
				<-readyTimer.C
			}
			cleanupReceive()
			s.closeTalkback(talkback)
			return runCtx.Err()
		}
	default:
		s.closeTalkback(talkback)
		return fmt.Errorf("unsupported receive mode %q", s.cfg.ReceiveMode())
	}
	if s.logger != nil {
		s.logger.Info("Reolink receive active", "mode", receiveInfo.Mode, "details", receiveInfo.Details)
	}

	// The RTP socket is deliberately bound before INVITE. Some PBXs (including
	// FRITZ!Box) can already send RTP during 183 Session Progress. Those packets
	// otherwise remain queued while the callee rings and while camera media is
	// negotiated, then arrive as a large artificial burst at the live bridge.
	discarded, capped, drainErr := discardQueuedSIPRTP(s.rtpConn)
	if drainErr != nil {
		cleanupReceive()
		s.closeTalkback(talkback)
		return fmt.Errorf("discard queued pre-answer SIP RTP: %w", drainErr)
	}
	if discarded > 0 && s.logger != nil {
		s.logger.Debug("discarded queued pre-answer SIP RTP", "packets", discarded, "limit_reached", capped)
	}
	if capped && s.logger != nil {
		s.logger.Warn("pre-answer SIP RTP queue drain reached safety limit", "packets", discarded)
	}

	errCh := make(chan error, 4)
	var wg sync.WaitGroup
	mediaStarted := time.Now()
	if s.cfg.ReceiveMode() == "rtsp" {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errCh <- s.forwardCameraToPhone(runCtx, s.ffConn, s.rtpConn, echo, mediaStarted)
		}()
		go func() {
			defer wg.Done()
			select {
			case ffRunErr := <-ffErr:
				errCh <- ffRunErr
			case <-runCtx.Done():
				errCh <- runCtx.Err()
			}
		}()
	} else {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- s.forwardBaichuanToPhone(runCtx, bcReceiver, s.rtpConn, echo, mediaStarted)
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); errCh <- talkback.Run(runCtx, s.rtpConn, s.call, controls) }()

	echoInfo := "off"
	if echo != nil {
		echoInfo = fmt.Sprintf("native WebRTC APM, %d ms fixed coarse delay, tracking=%t", s.cfg.AECInitialDelayMS, productionAECDelayTracking)
	}
	s.signalReady(SessionInfo{Talkback: talkInfo, Receive: receiveInfo, EchoCancellation: echoInfo})

	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-errCh:
	}

	cancelRun()
	_ = s.rtpConn.Close()
	if s.ffConn != nil {
		_ = s.ffConn.Close()
	}
	cleanupReceive()
	wg.Wait()
	s.closeTalkback(talkback)
	if s.logger != nil {
		if n := s.ffmpegTimestampWarnings.Load(); n > 0 {
			s.logger.Debug("ffmpeg timestamp corrections during call", "non_monotonous_dts", n)
		}
	}

	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Session) closeTalkback(t talkbackTransport) {
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := t.Close(closeCtx); err != nil && s.logger != nil {
		s.logger.Debug("Reolink talkback shutdown failed", "error", err)
	}
}

func (s *Session) forwardCameraToPhone(ctx context.Context, in *net.UDPConn, out *net.UDPConn, echo *echoCanceller, mediaStarted time.Time) error {
	buf := make([]byte, 2048)
	firstPacketDeadline := time.Now().Add(12 * time.Second)
	receivedFirst := false
	packetizer := &rtpRepacketizer{}
	var captureClock rtpMediaClock
	var packets uint64
	var firstAudioDelay time.Duration
	var maxInterarrivalDeviation time.Duration
	var previousArrival time.Time
	var previousTimestamp uint32
	defer func() {
		if s.logger != nil {
			s.logger.Debug("camera RTP bridge stopped",
				"packets", packets,
				"first_audio_ms", float64(firstAudioDelay.Microseconds())/1000.0,
				"rtp_jitter_max_ms", float64(maxInterarrivalDeviation.Microseconds())/1000.0)
		}
	}()
	for {
		_ = in.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := in.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if !receivedFirst && time.Now().After(firstPacketDeadline) {
					return errors.New("no camera audio RTP received from ffmpeg within 12 seconds")
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
		p, err := rtp.Parse(buf[:n])
		if err != nil || len(p.Payload) == 0 {
			continue
		}
		now := time.Now()
		if !receivedFirst {
			receivedFirst = true
			firstAudioDelay = now.Sub(mediaStarted)
			if s.logger != nil {
				s.logger.Debug("first camera audio RTP received", "after_ms", float64(firstAudioDelay.Microseconds())/1000.0)
			}
		}
		if !previousArrival.IsZero() {
			tsDelta := int32(p.Timestamp - previousTimestamp)
			if tsDelta > 0 && tsDelta <= g711SampleRate*2 {
				expected := time.Duration(int64(tsDelta)) * time.Second / g711SampleRate
				deviation := now.Sub(previousArrival) - expected
				if deviation < 0 {
					deviation = -deviation
				}
				if deviation > maxInterarrivalDeviation {
					maxInterarrivalDeviation = deviation
				}
			}
		}
		previousArrival = now
		previousTimestamp = p.Timestamp
		packets++
		for _, pkt := range packetizer.Push(p, s.call.Codec.PayloadType) {
			if echo != nil {
				captureAt := captureClock.At(pkt, now)
				pcm := g711.DecodePayload(pkt.Payload, s.call.Codec.Name)
				processed, aecErr := echo.ProcessCapture(ctx, pcm, captureAt)
				if aecErr != nil {
					return fmt.Errorf("WebRTC echo cancellation: %w", aecErr)
				}
				pkt.Payload = g711.EncodePCM(processed, s.call.Codec.Name)
			}
			remote := s.call.RemoteRTPAddr()
			if remote == nil {
				return errors.New("missing remote RTP address")
			}
			if _, err = out.WriteToUDP(rtp.Marshal(pkt), remote); err != nil {
				return err
			}
		}
	}
}

type cameraPCMSource interface {
	PCM() <-chan []int16
	Done() <-chan error
}

func (s *Session) forwardBaichuanToPhone(ctx context.Context, source cameraPCMSource, out *net.UDPConn, echo *echoCanceller, mediaStarted time.Time) error {
	if source == nil {
		return errors.New("Baichuan receive source is not initialized")
	}
	playout := newCameraPlayoutPLL()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	seq, timestamp, ssrc := randomRTPState()
	marker := true
	started := false
	var (
		inputChunks     uint64
		packetsSent     uint64
		firstAudioDelay time.Duration
	)
	defer func() {
		if s.logger != nil {
			st := playout.Stats()
			s.logger.Debug("Baichuan camera audio bridge stopped",
				"input_chunks", inputChunks,
				"rtp_packets", packetsSent,
				"first_audio_ms", float64(firstAudioDelay.Microseconds())/1000.0,
				"smoother_target_ms", float64(st.TargetSamples)*1000.0/g711SampleRate,
				"smoother_startup_ms", float64(st.StartupSamples)*1000.0/g711SampleRate,
				"smoother_queue_min_ms", float64(st.QueueMinSamples)*1000.0/g711SampleRate,
				"smoother_queue_avg_ms", st.QueueAverageSamples*1000.0/g711SampleRate,
				"smoother_queue_max_ms", float64(st.QueueMaxSamples)*1000.0/g711SampleRate,
				"smoother_hard_dropped_samples", st.HardDroppedSamples,
				"smoother_underrun_output_samples", st.UnderrunOutputSamples,
				"smoother_rate_avg_ppm", st.AverageCorrectionPPM,
				"smoother_rate_current_ppm", st.CurrentCorrectionPPM,
				"smoother_rate_max_abs_ppm", st.MaximumCorrectionPPM,
				"smoother_input_timeline_rebases", st.InputTimelineRebases,
				"smoother_input_max_chunk_ms", float64(st.InputMaxChunkSamples)*1000.0/g711SampleRate,
				"smoother_input_max_arrival_gap_ms", float64(st.InputMaxArrivalGap.Microseconds())/1000.0)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case recvErr, ok := <-source.Done():
			if !ok {
				return errors.New("Baichuan camera receive ended unexpectedly")
			}
			if recvErr == nil || errors.Is(recvErr, context.Canceled) {
				return errors.New("Baichuan camera receive ended unexpectedly")
			}
			return recvErr
		case pcm, ok := <-source.PCM():
			if !ok {
				return errors.New("Baichuan camera PCM stream ended")
			}
			if len(pcm) == 0 {
				continue
			}
			inputChunks++
			arrival := time.Now()
			if !started {
				started = true
				firstAudioDelay = arrival.Sub(mediaStarted)
				if s.logger != nil {
					s.logger.Debug("first Baichuan camera PCM received", "after_ms", float64(firstAudioDelay.Microseconds())/1000.0)
				}
			}
			dropped, rebase := playout.Push(pcm, arrival)
			if echo != nil && (dropped > 0 || rebase) {
				echo.SuspendTracking(arrival, "camera receive timeline discontinuity")
			}
		case now := <-ticker.C:
			if !started || !playout.Ready() {
				continue
			}
			block, missing, captureAt, ready := playout.PopFrame(now)
			if !ready {
				continue
			}
			if echo != nil {
				if missing > 0 {
					echo.SuspendTracking(captureAt, "camera receive underrun")
				}
				processed, aecErr := echo.ProcessCapture(ctx, block, captureAt)
				if aecErr != nil {
					return fmt.Errorf("WebRTC echo cancellation: %w", aecErr)
				}
				block = processed
			}
			payload := g711.EncodePCM(block, s.call.Codec.Name)
			if len(payload) != cameraPlayoutFrameSamples {
				return fmt.Errorf("encode Baichuan camera PCM to %s produced %d bytes", s.call.Codec.Name, len(payload))
			}
			remote := s.call.RemoteRTPAddr()
			if remote == nil {
				return errors.New("missing remote RTP address")
			}
			packet := rtp.Packet{
				PayloadType: s.call.Codec.PayloadType,
				Marker:      marker,
				Sequence:    seq,
				Timestamp:   timestamp,
				SSRC:        ssrc,
				Payload:     payload,
			}
			if _, err := out.WriteToUDP(rtp.Marshal(packet), remote); err != nil {
				return err
			}
			marker = false
			seq++
			timestamp += cameraPlayoutFrameSamples
			packetsSent++
		}
	}
}

func randomRTPState() (uint16, uint32, uint32) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err == nil {
		return binary.BigEndian.Uint16(b[0:2]), binary.BigEndian.Uint32(b[2:6]), binary.BigEndian.Uint32(b[6:10])
	}
	// crypto/rand failure is extraordinarily unlikely. Keep the media path
	// operational with per-process time-derived values rather than failing a
	// door call solely because random seeding was unavailable.
	n := uint64(time.Now().UnixNano())
	return uint16(n), uint32(n >> 16), uint32(n >> 32)
}

func buildFFmpegArgs(inputURL string, port int, codec sip.Codec) []string {
	encoder := "pcm_alaw"
	pt := "8"
	if codec.Name == g711.PCMU {
		encoder = "pcm_mulaw"
		pt = "0"
	}
	// TCP is retained as the stable RTSP baseline. A/B measurements on the
	// target NVR showed no meaningful latency benefit from UDP or extra FFmpeg
	// low-latency probe/reorder flags, so those experimental controls were
	// removed rather than carrying complexity into the production path.
	output := fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=172", port)
	return []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-timeout", "10000000", "-rtsp_transport", "tcp",
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-i", inputURL,
		"-map", "0:a:0", "-vn", "-ac", "1", "-ar", "8000",
		"-c:a", encoder, "-f", "rtp", "-payload_type", pt,
		output,
	}
}

func (s *Session) startFFmpeg(ctx context.Context, port int, codec sip.Codec) (*exec.Cmd, <-chan error, error) {
	u := &url.URL{Scheme: "rtsp", Host: net.JoinHostPort(s.cfg.ReolinkHost, strconv.Itoa(s.cfg.ReolinkRTSPPort)), Path: s.cfg.ReolinkStreamPath, User: url.UserPassword(s.cfg.ReolinkUsername, s.cfg.ReolinkPassword)}
	args := buildFFmpegArgs(u.String(), port, codec)
	cmd := exec.CommandContext(ctx, s.cfg.FFmpegPath(), args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 4096), 256*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "Non-monotonous DTS") {
				s.ffmpegTimestampWarnings.Add(1)
			}
			if s.logger != nil {
				s.logger.Warn("ffmpeg", "message", s.redact(line))
			}
		}
		scanErr := scanner.Err()
		waitErr := cmd.Wait()
		switch {
		case ctx.Err() != nil:
			errCh <- ctx.Err()
		case waitErr != nil:
			errCh <- fmt.Errorf("ffmpeg exited: %w", waitErr)
		case scanErr != nil:
			errCh <- fmt.Errorf("read ffmpeg stderr: %w", scanErr)
		default:
			errCh <- errors.New("ffmpeg exited unexpectedly")
		}
		close(errCh)
	}()
	return cmd, errCh, nil
}

func (s *Session) redact(v string) string {
	// Remove complete RTSP userinfo first; FFmpeg may print a percent-encoded
	// input URL whose password representation differs from the original text.
	v = rtspUserInfoPattern.ReplaceAllString(v, "rtsp://***@")
	if s.cfg.ReolinkPassword != "" {
		v = strings.ReplaceAll(v, s.cfg.ReolinkPassword, "***")
		v = strings.ReplaceAll(v, url.PathEscape(s.cfg.ReolinkPassword), "***")
		v = strings.ReplaceAll(v, url.QueryEscape(s.cfg.ReolinkPassword), "***")
	}
	return v
}
