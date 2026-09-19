package standalone

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "reolink_gateway_session"

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// LoadAdminToken creates a separate, local-only bootstrap credential. It is
// never logged and never used as the companion-integration API token.
func LoadAdminToken(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, "admin-token")
	if raw, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(raw))
		b, e := base64.RawURLEncoding.DecodeString(token)
		if e != nil || len(b) != 32 {
			return "", errors.New("invalid admin-token file; restore it or explicitly remove it to generate a new token")
		}
		if e = os.Chmod(path, 0600); e != nil {
			return "", e
		}
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return LoadAdminToken(dataDir)
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = f.WriteString(token + "\n"); err != nil {
		os.Remove(path)
		return "", err
	}
	if err = f.Sync(); err != nil {
		os.Remove(path)
		return "", err
	}
	return token, nil
}

func (s *Server) signature(value string) string {
	mac := hmac.New(sha256.New, []byte(s.adminToken))
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) session(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 || len(c.Value) > 200 {
		return ""
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() >= expires {
		return ""
	}
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(s.signature("session:"+parts[0]+"."+parts[1]))) != 1 {
		return ""
	}
	return c.Value
}

func (s *Server) Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.session(r) == "" {
			jsonError(w, http.StatusUnauthorized, "Bitte anmelden.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && (origin.Scheme == "http" || origin.Scheme == "https") && origin.User == nil && strings.EqualFold(origin.Host, r.Host)
}

func (s *Server) checkWrite(w http.ResponseWriter, r *http.Request) bool {
	session := s.session(r)
	if !sameOrigin(r) || session == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.signature("csrf:"+session))) != 1 {
		jsonError(w, http.StatusForbidden, "Ungültige Sitzung. Bitte die Seite neu laden.")
		return false
	}
	return true
}

func localRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		jsonError(w, 405, "POST erforderlich.")
		return
	}
	if !sameOrigin(r) {
		jsonError(w, 403, "Ungültige Herkunft.")
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if err := decodeRequest(w, r, &request); err != nil {
		jsonError(w, 400, "Ungültige Anmeldung.")
		return
	}
	// A random 256-bit credential makes online guessing infeasible. A global
	// bounded delay budget also stops rapid login requests consuming resources.
	s.mu.Lock()
	now := time.Now()
	if now.Sub(s.loginWindow) > time.Minute {
		s.loginWindow = now
		s.loginAttempts = 0
	}
	s.loginAttempts++
	limited := s.loginAttempts > 30
	s.mu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "60")
		jsonError(w, 429, "Zu viele Anmeldeversuche. Bitte eine Minute warten.")
		return
	}
	if subtle.ConstantTimeCompare([]byte(request.Token), []byte(s.adminToken)) != 1 {
		jsonError(w, 401, "Der Zugangsschlüssel stimmt nicht.")
		return
	}
	nonce, err := randomToken()
	if err != nil {
		jsonError(w, 500, "Anmeldung fehlgeschlagen.")
		return
	}
	body := strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10) + "." + nonce
	value := body + "." + s.signature("session:"+body)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", MaxAge: 86400, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
