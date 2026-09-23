// Package mobilebridge is the gomobile boundary used by the Android host.
package mobilebridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/gatewayruntime"
	"github.com/vothmarkus/reolink-sip-gateway/internal/platformaudio"
	"github.com/vothmarkus/reolink-sip-gateway/internal/standalone"
	statuspkg "github.com/vothmarkus/reolink-sip-gateway/internal/status"
)

type PlatformAudio interface {
	StartAEC(highPass, noiseSuppression bool) (int64, error)
	ProcessAEC(handle int64, request []byte) ([]byte, error)
	StopAEC(handle int64)
	StartAACDecoder(sampleRate int32, channels int32) (int64, error)
	DecodeAAC(handle int64, adts []byte) ([]byte, error)
	StopAACDecoder(handle int64)
}

type Listener interface {
	OnState(state string)
	OnError(message string)
}

type Gateway struct {
	mu            sync.Mutex
	cancel        context.CancelFunc
	done          chan struct{}
	server        *http.Server
	imageServer   *http.Server
	imageListener net.Listener
	imagePath     string
	imagePort     int
	registrar     string
	listener      net.Listener
	baseURL       string
	token         string
	lastErr       string
	running       bool
}

// The audio adapter belongs to one runtime. A failed/slow stop must not let a
// second runtime replace it while the first one still owns decoder handles.
var activeRuntime struct {
	sync.Mutex
	active bool
}

func Version() string { return gatewayruntime.Version }

func ValidateConfig(raw string) error {
	settings, err := standalone.DecodeSettings([]byte(raw))
	if err != nil {
		return err
	}
	return validateAndroidSettings(settings)
}

func Start(raw, dataDir string, audio PlatformAudio, listener Listener) (*Gateway, error) {
	settings, err := standalone.DecodeSettings([]byte(raw))
	if err != nil {
		return nil, err
	}
	if err := validateAndroidSettings(settings); err != nil {
		return nil, err
	}
	cfg, err := settings.Runtime(false)
	if err != nil {
		return nil, err
	}
	activeRuntime.Lock()
	if activeRuntime.active {
		activeRuntime.Unlock()
		return nil, errors.New("an Android gateway is already running or stopping")
	}
	activeRuntime.active = true
	activeRuntime.Unlock()
	started := false
	defer func() {
		if !started {
			releaseRuntime()
		}
	}()
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("Android data directory is empty")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create Android data directory: %w", err)
	}
	if cfg.EchoCancellationEnabled && audio == nil {
		return nil, errors.New("Android WebRTC AEC needs the platform audio adapter")
	}
	cfg.DataDir = dataDir
	identity, err := statuspkg.LoadOrCreateIdentity(dataDir)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open Android loopback API: %w", err)
	}
	var imageListener net.Listener
	if cfg.FritzFonLiveImageEnabled {
		imageListener, err = net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", cfg.StatusPort))
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("open Android live image port %d: %w", cfg.StatusPort, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{
		cancel: cancel, done: make(chan struct{}), listener: ln,
		baseURL: "http://" + ln.Addr().String(), token: identity.Token, running: true,
		imageListener: imageListener, imagePort: cfg.StatusPort,
		registrar: net.JoinHostPort(cfg.SIPRegistrar, fmt.Sprint(cfg.SIPRegistrarPort)),
	}
	if imageListener != nil {
		g.imagePath = "/fritzfon/" + identity.LiveImageToken + ".jpg"
	}
	restoreAudio := platformaudio.Set(audio)
	started = true
	notifyState(listener, "starting")
	go func() {
		defer close(g.done)
		defer releaseRuntime()
		defer restoreAudio()
		defer ln.Close()
		if imageListener != nil {
			defer imageListener.Close()
		}
		defer func() {
			g.mu.Lock()
			srv := g.server
			imageSrv := g.imageServer
			g.mu.Unlock()
			if srv != nil {
				_ = srv.Close()
			}
			if imageSrv != nil {
				_ = imageSrv.Close()
			}
		}()
		err := gatewayruntime.Run(ctx, cfg, gatewayruntime.Options{
			Publish: func(handler http.Handler) {
				serve := func(ln net.Listener, h http.Handler, image bool) {
					srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
					g.mu.Lock()
					if image {
						g.imageServer = srv
					} else {
						g.server = srv
					}
					g.mu.Unlock()
					go func() {
						if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
							g.setError(serveErr.Error())
							notifyError(listener, serveErr.Error())
							cancel()
						}
					}()
				}
				serve(ln, handler, false)
				if imageListener != nil {
					serve(imageListener, imageOnlyHandler(handler, g.imagePath), true)
				}
			},
		})
		g.mu.Lock()
		g.running = false
		if err != nil && !errors.Is(err, context.Canceled) {
			g.lastErr = err.Error()
		}
		g.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			notifyError(listener, err.Error())
		}
		notifyState(listener, "stopped")
	}()
	return g, nil
}

func validateAndroidSettings(settings standalone.Settings) error {
	mode := strings.ToLower(strings.TrimSpace(settings.Reolink.Mode))
	if mode != "nvr" && mode != "direct" {
		return errors.New("Android requires direct (camera without NVR) or nvr mode; RTSP/auto need the Linux host")
	}
	if strings.EqualFold(strings.TrimSpace(settings.SIP.Registrar), "auto") {
		return errors.New("Android alpha requires an explicit SIP registrar/FRITZ!Box address")
	}
	if !settings.Audio.AEC && (settings.Audio.HighPass || settings.Audio.NoiseSuppression) {
		return errors.New("Android WebRTC filters require echo cancellation to be enabled")
	}
	if settings.LiveImage.Port < 1024 || settings.LiveImage.Port > 65535 {
		return errors.New("Android live image port must be 1024..65535")
	}
	_, err := settings.Runtime(false)
	return err
}

func releaseRuntime() {
	activeRuntime.Lock()
	activeRuntime.active = false
	activeRuntime.Unlock()
}

func (g *Gateway) IsRunning() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

func (g *Gateway) LastError() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastErr
}

func (g *Gateway) Stop() error {
	if g == nil {
		return nil
	}
	g.cancel()
	g.mu.Lock()
	srv := g.server
	g.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
	select {
	case <-g.done:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("gateway did not stop within 10 seconds")
	}
}

func (g *Gateway) StatusJSON() (string, error) {
	body, err := g.api(http.MethodGet, "/api/v1/status")
	return string(body), err
}

func (g *Gateway) TestCall(routeID string) error {
	if strings.TrimSpace(routeID) == "" {
		routeID = "default"
	}
	_, err := g.api(http.MethodPost, "/api/v1/routes/"+url.PathEscape(routeID)+"/test")
	return err
}

func (g *Gateway) Hangup() error {
	_, err := g.api(http.MethodPost, "/api/v1/calls/hangup")
	return err
}

func (g *Gateway) api(method, path string) ([]byte, error) {
	if g == nil {
		return nil, errors.New("gateway is nil")
	}
	req, err := http.NewRequest(method, g.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gateway API %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (g *Gateway) setError(message string) {
	g.mu.Lock()
	g.lastErr = message
	g.mu.Unlock()
}

func notifyState(listener Listener, state string) {
	if listener != nil {
		listener.OnState(state)
	}
}

func notifyError(listener Listener, message string) {
	if listener != nil {
		listener.OnError(message)
	}
}
