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
