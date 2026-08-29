package liveimage

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func TestFetchJPEGUsesReolinkCGIResizesAndCaches(t *testing.T) {
	large := testJPEG(t, 1280, 960)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		query := r.URL.Query()
		if r.URL.Path != "/cgi-bin/api.cgi" || query.Get("cmd") != "Snap" || query.Get("channel") != "2" ||
			query.Get("user") != "camera-user" || query.Get("password") != "camera-secret" ||
			query.Get("width") != "640" || query.Get("height") != "480" || query.Get("rs") == "" {
			t.Errorf("unexpected snapshot request: path=%q query=%v", r.URL.Path, query)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(large)
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.ReolinkUsername = "camera-user"
	cfg.ReolinkPassword = "camera-secret"
	cfg.NVRChannel = 2
	source := New(cfg, nil)
	source.client = server.Client()
	source.cgiBases = []string{server.URL}
	source.fallback = func(context.Context) ([]byte, error) {
		t.Fatal("RTSP fallback was called after a successful CGI response")
		return nil, nil
	}
	fixedNow := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	source.now = func() time.Time { return fixedNow }

	first, err := source.FetchJPEG(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.DecodeConfig(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Width != 480 || decoded.Height != 360 {
		t.Fatalf("normalized size=%dx%d", decoded.Width, decoded.Height)
	}
	first[2] ^= 0xff
	second, err := source.FetchJPEG(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("CGI request count=%d, want one cached request", requests.Load())
	}
	if bytes.Equal(first, second) {
		t.Fatal("caller mutation changed the cached JPEG")
	}
}

func TestFetchJPEGFallsBackToRTSP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"code":1}]`))
	}))
	defer server.Close()

	source := New(config.Defaults(), nil)
	source.client = server.Client()
	source.cgiBases = []string{server.URL}
	fallbackCalls := 0
	source.fallback = func(context.Context) ([]byte, error) {
		fallbackCalls++
		return testJPEG(t, 320, 240), nil
	}

	imageBytes, err := source.FetchJPEG(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fallbackCalls != 1 || len(imageBytes) == 0 {
		t.Fatalf("fallback calls=%d bytes=%d", fallbackCalls, len(imageBytes))
	}
	diagnostics := source.Diagnostics()
	if diagnostics.LastFailed || diagnostics.LastSource != "RTSP" || diagnostics.LastAttempt.IsZero() || diagnostics.LastSuccess.IsZero() {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
}

func TestPrewarmServesFirstDelayedRequestAndThenReturnsToShortCache(t *testing.T) {
	snapshot := testJPEG(t, 320, 240)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(snapshot)
	}))
	defer server.Close()

	source := New(config.Defaults(), nil)
	source.client = server.Client()
	source.cgiBases = []string{server.URL}
	source.fallback = nil
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	source.now = func() time.Time { return now }

	if err := source.Prewarm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("prewarm requests=%d", requests.Load())
	}
	now = now.Add(3 * time.Second)
	if _, err := source.FetchJPEG(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := source.FetchJPEG(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("prewarmed HEAD/GET pair made %d requests", requests.Load())
	}
	now = now.Add(cacheLifetime + time.Millisecond)
	if _, err := source.FetchJPEG(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("normal refresh requests=%d", requests.Load())
	}
}

func TestFetchJPEGDoesNotExposeCameraPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.ReolinkPassword = "very-secret-password"
	source := New(cfg, nil)
	source.client = server.Client()
	source.cgiBases = []string{server.URL}
	source.fallback = func(context.Context) ([]byte, error) {
		return nil, errors.New("RTSP snapshot fallback failed")
	}
	_, err := source.FetchJPEG(context.Background())
	if err == nil {
		t.Fatal("expected capture failure")
	}
	if strings.Contains(err.Error(), cfg.ReolinkPassword) {
		t.Fatalf("password leaked in error: %v", err)
	}
	diagnostics := source.Diagnostics()
	if !diagnostics.LastFailed || diagnostics.LastAttempt.IsZero() || !diagnostics.LastSuccess.IsZero() {
		t.Fatalf("failure diagnostics=%#v", diagnostics)
	}
}

func TestFitDimensionsPreservesAspectRatio(t *testing.T) {
	tests := []struct {
		width, height int
		wantWidth     int
		wantHeight    int
	}{
		{1280, 960, 480, 360},
		{1920, 1080, 480, 270},
		{1080, 1920, 360, 640},
		{320, 240, 320, 240},
	}
	for _, test := range tests {
		gotWidth, gotHeight := fitDimensions(test.width, test.height, targetWidth, targetHeight)
		if gotWidth != test.wantWidth || gotHeight != test.wantHeight {
			t.Fatalf("fit %dx%d = %dx%d, want %dx%d", test.width, test.height, gotWidth, gotHeight, test.wantWidth, test.wantHeight)
		}
	}
}

func TestSameHostRedirectsRejectsCredentialForwardingToAnotherHost(t *testing.T) {
	original, err := http.NewRequest(http.MethodGet, "https://camera.local/cgi-bin/api.cgi?password=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := http.NewRequest(http.MethodGet, "https://attacker.invalid/snapshot.jpg", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sameHostRedirects(redirect, []*http.Request{original}); err == nil {
		t.Fatal("cross-host redirect was accepted")
	}
}

func TestChannelRTSPURLKeepsPrimaryAndBuildsOtherNVRPaths(t *testing.T) {
	cfg := config.Defaults()
	cfg.ReolinkHost = "192.168.177.82"
	cfg.ReolinkRTSPPort = 8554
	cfg.NVRChannel = 1
	cfg.ReolinkStreamPath = "/Preview_02_sub"
	if got := channelRTSPURL(cfg, 1); got != cfg.RTSPURL() {
		t.Fatalf("primary RTSP URL=%q", got)
	}
	if got := channelRTSPURL(cfg, 2); got != "rtsp://192.168.177.82:8554/Preview_03_sub" {
		t.Fatalf("channel 3 RTSP URL=%q", got)
	}
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 96, A: 255})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
