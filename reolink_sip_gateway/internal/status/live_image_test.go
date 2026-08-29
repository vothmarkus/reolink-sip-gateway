package status

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type staticJPEGProvider struct {
	image []byte
	err   error
	calls int
}

type staticMultiJPEGProvider struct {
	staticJPEGProvider
	channels     []LiveImageChannel
	discovery    LiveImageDiscovery
	channelCalls []int
	cameraCalls  []string
}

func (p *staticMultiJPEGProvider) LiveImageChannels() []LiveImageChannel {
	return append([]LiveImageChannel(nil), p.channels...)
}

func (p *staticMultiJPEGProvider) LiveImageDiscovery() LiveImageDiscovery {
	return p.discovery
}

func (p *staticMultiJPEGProvider) FetchChannelJPEG(_ context.Context, channel int) ([]byte, error) {
	p.channelCalls = append(p.channelCalls, channel)
	return p.image, p.err
}

func (p *staticMultiJPEGProvider) FetchCameraJPEG(_ context.Context, cameraID string) ([]byte, error) {
	p.cameraCalls = append(p.cameraCalls, cameraID)
	return p.image, p.err
}

func (p *staticJPEGProvider) FetchJPEG(context.Context) ([]byte, error) {
	p.calls++
	return p.image, p.err
}

func TestLiveImageHandlerReturnsCacheFreeJPEG(t *testing.T) {
	provider := &staticJPEGProvider{image: []byte{0xff, 0xd8, 0xff, 0xd9}}
	handler := liveImageHandler(provider)
	req := httptest.NewRequest(http.MethodGet, "/ignored.jpg", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "image/jpeg" ||
		!stringsContainAll(res.Header().Get("Cache-Control"), "no-store", "no-cache") ||
		!bytes.Equal(res.Body.Bytes(), provider.image) {
		t.Fatalf("unexpected response: status=%d headers=%v body=%x", res.Code, res.Header(), res.Body.Bytes())
	}
}

func TestLiveImageHandlerSupportsHEADAndRejectsOtherMethods(t *testing.T) {
	provider := &staticJPEGProvider{image: []byte{0xff, 0xd8, 0xff, 0xd9}}
	handler := liveImageHandler(provider)

	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/snapshot.jpg", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "4" {
		t.Fatalf("HEAD status=%d length=%q body=%x", head.Code, head.Header().Get("Content-Length"), head.Body.Bytes())
	}

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/snapshot.jpg", nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", post.Code, post.Header().Get("Allow"))
	}
}

func TestLiveImageEndpointIsLimitedToLocalNetworks(t *testing.T) {
	provider := &staticJPEGProvider{image: []byte{0xff, 0xd8, 0xff, 0xd9}}
	handler := localLiveImageOnly(liveImageHandler(provider))

	publicRequest := httptest.NewRequest(http.MethodGet, "/snapshot.jpg", nil)
	publicRequest.RemoteAddr = "203.0.113.10:1234"
	publicResponse := httptest.NewRecorder()
	handler.ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusForbidden || provider.calls != 0 {
		t.Fatalf("public request status=%d provider calls=%d", publicResponse.Code, provider.calls)
	}

	privateRequest := httptest.NewRequest(http.MethodGet, "/snapshot.jpg", nil)
	privateRequest.RemoteAddr = "192.168.177.9:1234"
	privateResponse := httptest.NewRecorder()
	handler.ServeHTTP(privateResponse, privateRequest)
	if privateResponse.Code != http.StatusOK || provider.calls != 1 {
		t.Fatalf("private request status=%d provider calls=%d", privateResponse.Code, provider.calls)
	}
}

func TestLiveImageRouteRequiresExactTokenizedPath(t *testing.T) {
	provider := &staticJPEGProvider{image: []byte{0xff, 0xd8, 0xff, 0xd9}}
	mux := http.NewServeMux()
	mux.Handle(liveImagePath("correct-token"), localLiveImageOnly(liveImageHandler(provider)))

	request := httptest.NewRequest(http.MethodGet, "/fritzfon/wrong-token.jpg", nil)
	request.RemoteAddr = "192.168.177.9:1234"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || provider.calls != 0 {
		t.Fatalf("wrong path status=%d provider calls=%d", response.Code, provider.calls)
	}
}

func TestLiveImageHandlerFailsClosed(t *testing.T) {
	for name, provider := range map[string]*staticJPEGProvider{
		"capture error": {err: errors.New("camera password=secret")},
		"not jpeg":      {image: []byte("not an image")},
	} {
		t.Run(name, func(t *testing.T) {
			res := httptest.NewRecorder()
			liveImageHandler(provider).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/snapshot.jpg", nil))
			if res.Code != http.StatusBadGateway || bytes.Contains(res.Body.Bytes(), []byte("secret")) {
				t.Fatalf("status=%d body=%q", res.Code, res.Body.String())
			}
		})
	}
}

func TestLiveImageAddressEndsInJPEG(t *testing.T) {
	got := liveImageAddress("192.168.177.5", 18099, "secret-token")
	if got != "192.168.177.5:18099/fritzfon/secret-token.jpg" || fullLiveImageURL(got) != "http://"+got {
		t.Fatalf("unexpected address %q", got)
	}
}

func TestLiveImageAddressHasExplicitHostPlaceholder(t *testing.T) {
	got := liveImageAddress("", 18099, "secret-token")
	if got != "HOME-ASSISTANT-IP:18099/fritzfon/secret-token.jpg" {
		t.Fatalf("unexpected fallback address %q", got)
	}
}

func TestMultiChannelLiveImageRouteServesOnlyPublishedExactPaths(t *testing.T) {
	provider := &staticMultiJPEGProvider{
		staticJPEGProvider: staticJPEGProvider{image: []byte{0xff, 0xd8, 0xff, 0xd9}},
		channels:           []LiveImageChannel{{Number: 1}, {Number: 2}},
	}
	handler := liveImageChannelHandler(provider, "secret-token")

	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, httptest.NewRequest(http.MethodGet, "/fritzfon/secret-token/channel-2.jpg", nil))
	if valid.Code != http.StatusOK || !bytes.Equal(valid.Body.Bytes(), provider.image) || len(provider.channelCalls) != 1 || provider.channelCalls[0] != 2 {
		t.Fatalf("valid response status=%d calls=%v body=%x", valid.Code, provider.channelCalls, valid.Body.Bytes())
	}

	for _, path := range []string{
		"/fritzfon/secret-token/channel-3.jpg",
		"/fritzfon/secret-token/channel-0.jpg",
		"/fritzfon/secret-token/channel-02.jpg",
		"/fritzfon/secret-token/channel-2.png",
		"/fritzfon/secret-token/channel-2.jpg/extra",
		"/fritzfon/wrong-token/channel-2.jpg",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("path %q status=%d", path, response.Code)
		}
	}
	if len(provider.channelCalls) != 1 {
		t.Fatalf("unpublished paths reached provider: calls=%v", provider.channelCalls)
	}
}

func TestStableCameraRouteServesOnlyPublishedExactIDs(t *testing.T) {
	cameraID := "0123456789abcdef0123456789abcdef"
	provider := &staticMultiJPEGProvider{
		staticJPEGProvider: staticJPEGProvider{image: []byte{0xff, 0xd8, 0xff, 0xd9}},
		channels:           []LiveImageChannel{{Number: 2, CameraID: cameraID}},
	}
	handler := liveImageChannelHandler(provider, "secret-token")
	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, httptest.NewRequest(http.MethodGet, "/fritzfon/secret-token/camera-"+cameraID+".jpg", nil))
	if valid.Code != http.StatusOK || len(provider.cameraCalls) != 1 || provider.cameraCalls[0] != cameraID {
		t.Fatalf("stable route status=%d calls=%v", valid.Code, provider.cameraCalls)
	}
	for _, invalidID := range []string{
		"0123456789abcdef", "0123456789abcdef0123456789abcdeg", "0123456789ABCDEF0123456789ABCDEF",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/fritzfon/secret-token/camera-"+invalidID+".jpg", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("invalid camera ID %q status=%d", invalidID, response.Code)
		}
	}
	if len(provider.cameraCalls) != 1 {
		t.Fatalf("invalid IDs reached provider: %v", provider.cameraCalls)
	}
}

func TestMultiChannelAddressesAndPageEntriesAreStableAndSorted(t *testing.T) {
	cameraID := "0123456789abcdef0123456789abcdef"
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	provider := &staticMultiJPEGProvider{
		staticJPEGProvider: staticJPEGProvider{image: []byte{0xff, 0xd8, 0xff, 0xd9}},
		channels: []LiveImageChannel{
			{Number: 2, Name: "Türklingel", CameraID: cameraID, Online: true, StatusKnown: true, LastImageAttempt: now, LastImageSuccess: now, LastImageSource: "HTTPS", LastImageDuration: 42 * time.Millisecond},
			{Number: 1, Name: "Einfahrt", StatusKnown: true},
			{Number: 2}, {Number: 0}, {Number: 300},
		},
		discovery: LiveImageDiscovery{LastAttempt: now, LastSuccess: now},
	}
	options := ServerOptions{
		Port: 18099, LiveImageHost: "192.168.177.5", LiveImageToken: "secret-token", LiveImageProvider: provider,
	}
	entries := liveImagePageChannels(options)
	if len(entries) != 2 || entries[0].Number != 1 || entries[1].Number != 2 || entries[1].Name != "Türklingel" {
		t.Fatalf("page entries=%#v", entries)
	}
	if entries[0].Address != "" || entries[0].ChannelAddress != "192.168.177.5:18099/fritzfon/secret-token/channel-1.jpg" ||
		entries[1].Address != "192.168.177.5:18099/fritzfon/secret-token/camera-"+cameraID+".jpg" ||
		entries[1].URL != "http://"+entries[1].Address || entries[1].StatusText != "online" || !strings.Contains(entries[1].CaptureText, "HTTPS (42 ms)") {
		t.Fatalf("channel addresses=%#v", entries)
	}
	placeholder := liveImageChannelAddress("", 18099, "secret-token", 2)
	if placeholder != "HOME-ASSISTANT-IP:18099/fritzfon/secret-token/channel-2.jpg" {
		t.Fatalf("placeholder address=%q", placeholder)
	}
	discovery := liveImagePageDiscovery(options)
	if discovery.StatusText != "erfolgreich" || discovery.StatusClass != "ok" || discovery.LastSuccess != now {
		t.Fatalf("discovery page data=%#v", discovery)
	}
}

func stringsContainAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !bytes.Contains([]byte(value), []byte(fragment)) {
			return false
		}
	}
	return true
}
