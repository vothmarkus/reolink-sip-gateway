package mobilebridge

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Only the primary tokenized JPEG route is reachable from the LAN. In
// particular, the setup UI and all authenticated API routes stay loopback-only.
// The shared image handler additionally enforces local-network source IPs.
func imageOnlyHandler(next http.Handler, path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path == "" || r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LiveImageURL is an explicit UI action: the capability URL is never part of
// the status JSON, diagnostic clipboard, notifications or logs.
func (g *Gateway) LiveImageURL() (string, error) {
	if g == nil || !g.IsRunning() || g.imagePath == "" {
		return "", errors.New("Live-Bild aktivieren und Gateway starten")
	}
	conn, err := net.DialTimeout("udp4", g.registrar, 2*time.Second)
	if err != nil {
		return "", errors.New("Lokale IPv4-Adresse für das Live-Bild nicht verfügbar")
	}
	defer conn.Close()
	address, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || address.IP.IsUnspecified() || address.IP.IsLoopback() {
		return "", errors.New("Keine LAN-Adresse für das Live-Bild")
	}
	return "http://" + net.JoinHostPort(address.IP.String(), fmt.Sprint(g.imagePort)) + g.imagePath, nil
}

func (g *Gateway) SnapshotJPEG() ([]byte, error) {
	if g == nil || !g.IsRunning() || g.imagePath == "" {
		return nil, errors.New("Live-Bild aktivieren, speichern und Gateway starten")
	}
	client := &http.Client{Timeout: 18 * time.Second}
	response, err := client.Get(g.baseURL + g.imagePath)
	if err != nil {
		return nil, errors.New("Bildabruf fehlgeschlagen; Gateway und Kamera prüfen")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Kein Kamerabild. HTTP oder HTTPS an der Reolink-Kamera aktivieren und Zugangsdaten prüfen")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || len(body) < 4 || body[0] != 0xff || body[1] != 0xd8 {
		return nil, errors.New("Ungültiges Kamerabild")
	}
	return body, nil
}
