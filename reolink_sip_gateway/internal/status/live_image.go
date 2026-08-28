package status

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const liveImagePrefix = "/fritzfon/"

// JPEGProvider is deliberately tiny so the public HTTP server does not depend
// on Reolink transport details and can be tested independently.
type JPEGProvider interface {
	FetchJPEG(context.Context) ([]byte, error)
}

// MultiChannelJPEGProvider is optional. Providers implementing it keep the
// legacy primary image while additionally exposing automatically discovered,
// public 1-based NVR channels below the same secret path token.
type MultiChannelJPEGProvider interface {
	JPEGProvider
	ChannelNumbers() []int
	ChannelName(int) string
	FetchChannelJPEG(context.Context, int) ([]byte, error)
}

func liveImagePath(token string) string {
	return liveImagePrefix + token + ".jpg"
}

func liveImageAddress(host string, port int, token string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "HOME-ASSISTANT-IP:" + strconv.Itoa(port) + liveImagePath(token)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)) + liveImagePath(token)
}

func liveImageChannelPrefix(token string) string {
	return liveImagePrefix + token + "/"
}

func liveImageChannelPath(token string, channel int) string {
	return liveImageChannelPrefix(token) + "channel-" + strconv.Itoa(channel) + ".jpg"
}

func liveImageChannelAddress(host string, port int, token string, channel int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "HOME-ASSISTANT-IP:" + strconv.Itoa(port) + liveImageChannelPath(token, channel)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)) + liveImageChannelPath(token, channel)
}

func liveImageHandler(provider JPEGProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setLiveImageResponseHeaders(w)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		imageBytes, err := provider.FetchJPEG(ctx)
		if err != nil || len(imageBytes) < 4 || imageBytes[0] != 0xff || imageBytes[1] != 0xd8 {
			http.Error(w, "live image temporarily unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(imageBytes)))
		w.Header().Set("Content-Disposition", `inline; filename="snapshot.jpg"`)
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(imageBytes)
		}
	})
}

func liveImageChannelHandler(provider MultiChannelJPEGProvider, token string) http.Handler {
	prefix := liveImageChannelPrefix(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		channel, ok := requestedLiveImageChannel(r.URL.Path, prefix)
		if !ok || !availableLiveImageChannel(provider.ChannelNumbers(), channel) {
			setLiveImageResponseHeaders(w)
			http.NotFound(w, r)
			return
		}
		liveImageHandler(channelJPEGProvider{provider: provider, channel: channel}).ServeHTTP(w, r)
	})
}

type channelJPEGProvider struct {
	provider MultiChannelJPEGProvider
	channel  int
}

func (p channelJPEGProvider) FetchJPEG(ctx context.Context) ([]byte, error) {
	return p.provider.FetchChannelJPEG(ctx, p.channel)
}

func requestedLiveImageChannel(path, prefix string) (int, bool) {
	if !strings.HasPrefix(path, prefix) {
		return 0, false
	}
	filename := strings.TrimPrefix(path, prefix)
	if !strings.HasPrefix(filename, "channel-") || !strings.HasSuffix(filename, ".jpg") {
		return 0, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(filename, "channel-"), ".jpg")
	if value == "" || strings.Contains(value, "/") {
		return 0, false
	}
	channel, err := strconv.Atoi(value)
	return channel, err == nil && channel >= 1 && channel <= 256 && value == strconv.Itoa(channel)
}

func availableLiveImageChannel(channels []int, requested int) bool {
	for _, channel := range channels {
		if channel == requested {
			return true
		}
	}
	return false
}

func localLiveImageOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedAPIRemote(r.RemoteAddr) {
			setLiveImageResponseHeaders(w)
			http.Error(w, "live image access is limited to local networks", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setLiveImageResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func fullLiveImageURL(address string) string {
	return fmt.Sprintf("http://%s", address)
}
