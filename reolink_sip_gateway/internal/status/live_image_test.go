package status

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type staticJPEGProvider struct {
	image []byte
	err   error
	calls int
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

func stringsContainAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !bytes.Contains([]byte(value), []byte(fragment)) {
			return false
		}
	}
	return true
}
