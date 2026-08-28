package liveimage

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

const (
	targetWidth          = 480
	targetHeight         = 640
	reolinkRequestWidth  = 640
	reolinkRequestHeight = 480
	maxSnapshotBytes     = 16 << 20
	maxDecodedPixels     = 32_000_000
	cacheLifetime        = 750 * time.Millisecond
)

// Source obtains one current Reolink frame and normalizes oversized JPEGs for
// the small FRITZ!Fon display. Camera credentials are used only on the
// gateway-to-Reolink hop and never reach HTTP clients. A short in-memory cache
// coalesces simultaneous requests from multiple phones.
type Source struct {
	cfg      config.Config
	logger   *slog.Logger
	client   *http.Client
	cgiBases []string
	fallback func(context.Context) ([]byte, error)
	now      func() time.Time
	mu       sync.Mutex
	cached   []byte
	cachedAt time.Time
}

func New(cfg config.Config, logger *slog.Logger) *Source {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
		TLSClientConfig: &tls.Config{
			// Reolink cameras and NVRs normally use a local self-signed
			// certificate. The configured host and credentials already define
			// the trusted device boundary; certificate PKI is not available.
			InsecureSkipVerify: true, //nolint:gosec
			MinVersion:         tls.VersionTLS12,
		},
	}
	s := &Source{
		cfg:      cfg,
		logger:   logger,
		client:   &http.Client{Transport: transport, Timeout: 5 * time.Second},
		cgiBases: snapshotCGIBases(cfg.ReolinkHost),
		now:      time.Now,
	}
	s.client.CheckRedirect = sameHostRedirects
	s.fallback = s.fetchRTSP
	return s
}

// FetchJPEG returns a current, valid JPEG. Calls inside cacheLifetime share the
// same immutable byte slice internally and receive their own copy.
func (s *Source) FetchJPEG(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if len(s.cached) > 0 && now.Sub(s.cachedAt) >= 0 && now.Sub(s.cachedAt) <= cacheLifetime {
		return append([]byte(nil), s.cached...), nil
	}

	imageBytes, err := s.fetchFresh(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("FRITZ!Fon live image capture failed", "error", err)
		}
		return nil, errors.New("FRITZ!Fon live image is temporarily unavailable")
	}
	s.cached = append(s.cached[:0], imageBytes...)
	s.cachedAt = s.now()
	return append([]byte(nil), s.cached...), nil
}

func (s *Source) fetchFresh(ctx context.Context) ([]byte, error) {
	var attempts []error
	for _, base := range s.cgiBases {
		imageBytes, err := s.fetchCGI(ctx, base)
		if err == nil {
			if normalized, normalizeErr := normalizeJPEG(imageBytes); normalizeErr == nil {
				return normalized, nil
			} else {
				err = normalizeErr
			}
		}
		attempts = append(attempts, err)
	}
	if s.fallback != nil {
		imageBytes, err := s.fallback(ctx)
		if err == nil {
			return normalizeJPEG(imageBytes)
		}
		attempts = append(attempts, err)
	}
	if len(attempts) == 0 {
		return nil, errors.New("no live image source is configured")
	}
	return nil, errors.Join(attempts...)
}

func (s *Source) fetchCGI(ctx context.Context, base string) ([]byte, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid Reolink CGI endpoint")
	}
	u.Path = "/cgi-bin/api.cgi"
	u.RawQuery = ""
	query := u.Query()
	query.Set("cmd", "Snap")
	query.Set("channel", strconv.Itoa(s.cfg.NVRChannel))
	query.Set("rs", strconv.FormatInt(time.Now().UnixNano(), 36))
	query.Set("user", s.cfg.ReolinkUsername)
	query.Set("password", s.cfg.ReolinkPassword)
	query.Set("width", strconv.Itoa(reolinkRequestWidth))
	query.Set("height", strconv.Itoa(reolinkRequestHeight))
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s Reolink CGI request could not be created", u.Scheme)
	}
	req.Header.Set("Accept", "image/jpeg")
	req.Header.Set("User-Agent", "ReolinkSIPGateway/1.3.0")
	response, err := s.client.Do(req)
	if err != nil {
		// net/http errors can include the full URL, including its password.
		// Keep the public/loggable error deliberately classified but opaque.
		return nil, fmt.Errorf("%s Reolink CGI connection failed", u.Scheme)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s Reolink CGI returned HTTP %d", u.Scheme, response.StatusCode)
	}
	imageBytes, err := io.ReadAll(io.LimitReader(response.Body, maxSnapshotBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s Reolink CGI response could not be read", u.Scheme)
	}
	if len(imageBytes) == 0 || len(imageBytes) > maxSnapshotBytes {
		return nil, fmt.Errorf("%s Reolink CGI returned an invalid image size", u.Scheme)
	}
	return imageBytes, nil
}

func (s *Source) fetchRTSP(ctx context.Context) ([]byte, error) {
	u, err := url.Parse(s.cfg.RTSPURL())
	if err != nil {
		return nil, errors.New("RTSP snapshot URL is invalid")
	}
	u.User = url.UserPassword(s.cfg.ReolinkUsername, s.cfg.ReolinkPassword)
	ffmpegCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-rtsp_transport", "tcp", "-timeout", "5000000",
		"-i", u.String(),
		"-map", "0:v:0", "-frames:v", "1", "-an", "-sn", "-dn",
		"-vf", "scale=480:640:force_original_aspect_ratio=decrease",
		"-q:v", "4", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1",
	}
	imageBytes, err := exec.CommandContext(ffmpegCtx, s.cfg.FFmpegPath(), args...).Output()
	if err != nil {
		return nil, fmt.Errorf("RTSP snapshot fallback failed: %v", commandResult(err))
	}
	if len(imageBytes) == 0 || len(imageBytes) > maxSnapshotBytes {
		return nil, errors.New("RTSP snapshot fallback returned an invalid image size")
	}
	return imageBytes, nil
}

func snapshotCGIBases(host string) []string {
	host = strings.TrimSpace(host)
	return []string{
		"https://" + net.JoinHostPort(host, "443"),
		"http://" + net.JoinHostPort(host, "80"),
	}
}

func sameHostRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("too many Reolink CGI redirects")
	}
	if len(via) > 0 && !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
		return errors.New("cross-host Reolink CGI redirect refused")
	}
	return nil
}

func normalizeJPEG(imageBytes []byte) ([]byte, error) {
	if len(imageBytes) < 4 || imageBytes[0] != 0xff || imageBytes[1] != 0xd8 {
		return nil, errors.New("snapshot response is not a JPEG")
	}
	decodedConfig, err := jpeg.DecodeConfig(bytes.NewReader(imageBytes))
	if err != nil || decodedConfig.Width <= 0 || decodedConfig.Height <= 0 {
		return nil, errors.New("snapshot response contains an invalid JPEG")
	}
	if decodedConfig.Width > maxDecodedPixels/decodedConfig.Height {
		return nil, errors.New("snapshot JPEG dimensions are too large")
	}
	if decodedConfig.Width <= targetWidth && decodedConfig.Height <= targetHeight {
		return imageBytes, nil
	}

	source, err := jpeg.Decode(bytes.NewReader(imageBytes))
	if err != nil {
		return nil, errors.New("snapshot JPEG could not be decoded")
	}
	newWidth, newHeight := fitDimensions(decodedConfig.Width, decodedConfig.Height, targetWidth, targetHeight)
	destination := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	bounds := source.Bounds()
	for y := 0; y < newHeight; y++ {
		sourceY := bounds.Min.Y + y*bounds.Dy()/newHeight
		for x := 0; x < newWidth; x++ {
			sourceX := bounds.Min.X + x*bounds.Dx()/newWidth
			destination.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, destination, &jpeg.Options{Quality: 82}); err != nil {
		return nil, errors.New("resized snapshot JPEG could not be encoded")
	}
	return output.Bytes(), nil
}

func fitDimensions(width, height, maximumWidth, maximumHeight int) (int, int) {
	if width <= maximumWidth && height <= maximumHeight {
		return width, height
	}
	newWidth := maximumWidth
	newHeight := height * maximumWidth / width
	if newHeight > maximumHeight {
		newHeight = maximumHeight
		newWidth = width * maximumHeight / height
	}
	if newWidth < 1 {
		newWidth = 1
	}
	if newHeight < 1 {
		newHeight = 1
	}
	return newWidth, newHeight
}

func commandResult(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ProcessState.String()
	}
	return "process error"
}
