package standalone

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

//go:embed web/*
var webFiles embed.FS

type Runner func(context.Context, config.Config, func(http.Handler)) error

type Server struct {
	ConfigPath    string
	DataDir       string
	Port          int
	Version       string
	APIToken      string
	Run           Runner
	Logger        *slog.Logger
	adminToken    string
	mu            sync.RWMutex
	settings      Settings
	revision      string
	diskHash      [32]byte
	hasFile       bool
	configured    bool
	lastError     string
	runtime       http.Handler
	reload        chan struct{}
	loginWindow   time.Time
	loginAttempts int
}

func New(configPath, dataDir string, port int, version, apiToken string, runner Runner, logger *slog.Logger) (*Server, error) {
	admin, err := LoadAdminToken(dataDir)
	if err != nil {
		return nil, err
	}
	revision, err := randomToken()
	if err != nil {
		return nil, err
	}
	s := &Server{ConfigPath: configPath, DataDir: dataDir, Port: port, Version: version, APIToken: apiToken, Run: runner, Logger: logger, adminToken: admin, revision: revision, settings: Defaults(), reload: make(chan struct{}, 1)}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		s.lastError = "Konfiguration kann nicht gelesen werden: " + err.Error()
		return s, nil
	}
	s.hasFile = true
	s.diskHash = sha256.Sum256(raw)
	settings, err := DecodeSettings(raw)
	if err == nil {
		_, err = settings.Runtime(false)
	}
	if err != nil {
		s.lastError = "Konfiguration prüfen: " + err.Error()
		return s, nil
	}
	s.settings = settings
	s.configured = true
	return s, nil
}

// Manage serializes runtime generations: stop and fully clean up the previous
// call/media runtime before opening its SIP ports with a new configuration.
func (s *Server) Manage(ctx context.Context) {
	for ctx.Err() == nil {
		s.mu.RLock()
		settings, configured := s.settings, s.configured
		s.mu.RUnlock()
		if !configured {
			select {
			case <-ctx.Done():
				return
			case <-s.reload:
				continue
			}
		}
		cfg, err := settings.Runtime(true)
		if err != nil {
			s.setError(err)
			if !s.waitRetry(ctx) {
				return
			}
			continue
		}
		cfg.DataDir, cfg.StatusPort = s.DataDir, s.Port
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		s.mu.Lock()
		s.lastError = ""
		s.mu.Unlock()
		go func() {
			done <- s.Run(runCtx, cfg, func(handler http.Handler) {
				s.mu.Lock()
				if runCtx.Err() == nil {
					s.runtime = handler
				}
				s.mu.Unlock()
			})
		}()
		select {
		case <-ctx.Done():
			cancel()
			s.clearRuntime()
			<-done
			return
		case <-s.reload:
			cancel()
			s.clearRuntime()
			<-done
			// Coalesce changes made while the old runtime was shutting down.
			select {
			case <-s.reload:
			default:
			}
		case err = <-done:
			cancel()
			s.clearRuntime()
			if err == nil {
				err = errors.New("gateway runtime stopped")
			}
			s.setError(err)
			if !s.waitRetry(ctx) {
				return
			}
		}
	}
}

func (s *Server) waitRetry(ctx context.Context) bool {
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.reload:
	case <-timer.C:
	}
	return true
}

func (s *Server) clearRuntime() { s.mu.Lock(); s.runtime = nil; s.mu.Unlock() }
func (s *Server) setError(err error) {
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
	if s.Logger != nil {
		s.Logger.Warn("gateway unavailable; setup remains accessible", "error", err)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if !localRequest(r) {
		jsonError(w, 403, "Nur im lokalen Netzwerk verfügbar.")
		return
	}
	switch r.URL.Path {
	case "/", "/app.js", "/style.css":
		if r.Method != "GET" && r.Method != "HEAD" {
			jsonError(w, 405, "GET erforderlich.")
			return
		}
		name := r.URL.Path
		if name == "/" {
			name = "/index.html"
		}
		content, err := webFiles.ReadFile("web" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		contentType := "text/html; charset=utf-8"
		if strings.HasSuffix(name, ".js") {
			contentType = "application/javascript; charset=utf-8"
		}
		if strings.HasSuffix(name, ".css") {
			contentType = "text/css; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		if r.Method != "HEAD" {
			_, _ = w.Write(content)
		}
	case "/health":
		writeJSON(w, 200, map[string]string{"status": "ok", "version": s.Version})
	case "/login":
		s.login(w, r)
	case "/admin/config":
		s.Auth(http.HandlerFunc(s.configuration)).ServeHTTP(w, r)
	case "/admin/export":
		s.Auth(http.HandlerFunc(s.export)).ServeHTTP(w, r)
	case "/admin/logout":
		s.Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				jsonError(w, 405, "POST erforderlich.")
				return
			}
			if !s.checkWrite(w, r) {
				return
			}
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
			writeJSON(w, 200, map[string]string{"status": "ok"})
		})).ServeHTTP(w, r)
	case "/admin/status", "/admin/hangup", "/status":
		s.Auth(http.HandlerFunc(s.adminProxy)).ServeHTTP(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/admin/test/") {
			s.Auth(http.HandlerFunc(s.adminProxy)).ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") || strings.HasPrefix(r.URL.Path, "/fritzfon/") || r.URL.Path == "/api/status" || r.URL.Path == "/logo.png" {
			s.proxy(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	handler := s.runtime
	message := s.lastError
	s.mu.RUnlock()
	if handler == nil {
		if message == "" {
			message = "Gateway wird eingerichtet oder neu gestartet."
		}
		// Unauthenticated clients never receive configuration/runtime details.
		if s.session(r) == "" {
			message = "Gateway unavailable"
		}
		jsonError(w, 503, message)
		return
	}
	handler.ServeHTTP(w, r)
}

func (s *Server) adminProxy(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/admin/status" || path == "/status" {
		if r.Method != "GET" {
			jsonError(w, 405, "GET erforderlich.")
			return
		}
	} else {
		if r.Method != "POST" {
			jsonError(w, 405, "POST erforderlich.")
			return
		}
		if !s.checkWrite(w, r) {
			return
		}
	}
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+s.APIToken)
	switch path {
	case "/admin/status":
		copy.URL.Path = "/api/v1/status"
	case "/status":
		copy.URL.Path = "/"
	case "/admin/hangup":
		copy.URL.Path = "/api/v1/calls/hangup"
	default:
		copy.URL.Path = "/api/v1/routes/" + strings.TrimPrefix(path, "/admin/test/") + "/test"
	}
	s.proxy(w, copy)
}

func (s *Server) configuration(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		s.mu.RLock()
		settings, revision, configured, lastError := s.settings, s.revision, s.configured, s.lastError
		s.mu.RUnlock()
		stored := map[string]bool{"reolink": settings.Reolink.Password != "", "sip": settings.SIP.Password != "", "parallel": settings.SIP.ParallelPassword != ""}
		settings.Reolink.Password, settings.SIP.Password, settings.SIP.ParallelPassword = "", "", ""
		writeJSON(w, 200, map[string]any{"config": settings, "revision": revision, "configured": configured, "error": lastError, "version": s.Version, "stored_secrets": stored, "csrf_token": s.signature("csrf:" + s.session(r))})
		return
	}
	if r.Method != "POST" {
		jsonError(w, 405, "GET oder POST erforderlich.")
		return
	}
	if !s.checkWrite(w, r) {
		return
	}
	var request struct {
		Config       json.RawMessage `json:"config"`
		Revision     string          `json:"revision"`
		ClearSecrets []string        `json:"clear_secrets"`
	}
	if err := decodeRequest(w, r, &request); err != nil {
		jsonError(w, 400, "Ungültige Konfiguration: "+err.Error())
		return
	}
	settings, err := DecodeSettings(request.Config)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	clear := map[string]bool{}
	for _, key := range request.ClearSecrets {
		if key != "reolink" && key != "sip" && key != "parallel" {
			jsonError(w, 400, "Unbekanntes Passwortfeld.")
			return
		}
		clear[key] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.Revision != s.revision {
		jsonError(w, 409, "Die Einstellungen wurden inzwischen geändert. Bitte neu laden.")
		return
	}
	if settings.Reolink.Password == "" && !clear["reolink"] {
		settings.Reolink.Password = s.settings.Reolink.Password
	}
	if settings.SIP.Password == "" && !clear["sip"] {
		settings.SIP.Password = s.settings.SIP.Password
	}
	if settings.SIP.ParallelPassword == "" && !clear["parallel"] {
		settings.SIP.ParallelPassword = s.settings.SIP.ParallelPassword
	}
	if _, err := settings.Runtime(false); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	raw, err := os.ReadFile(s.ConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		jsonError(w, 500, "Vorhandene Konfiguration kann nicht gelesen werden.")
		return
	}
	if (err == nil) != s.hasFile || (s.hasFile && sha256.Sum256(raw) != s.diskHash) {
		jsonError(w, 409, "Die Konfigurationsdatei wurde extern geändert. Bitte den Dienst neu starten und die Seite neu laden.")
		return
	}
	newRevision, err := randomToken()
	if err != nil {
		jsonError(w, 500, "Speichern fehlgeschlagen.")
		return
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		jsonError(w, 500, "Speichern fehlgeschlagen.")
		return
	}
	encoded = append(encoded, '\n')
	if s.hasFile {
		if err := atomicWrite(s.ConfigPath+".previous", raw); err != nil {
			jsonError(w, 500, "Vorherige Konfiguration konnte nicht gesichert werden.")
			return
		}
	}
	if err := atomicWrite(s.ConfigPath, encoded); err != nil {
		jsonError(w, 500, "Konfiguration konnte nicht gespeichert werden.")
		return
	}
	s.settings = settings
	s.revision = newRevision
	s.diskHash = sha256.Sum256(encoded)
	s.hasFile = true
	s.configured = true
	s.lastError = ""
	select {
	case s.reload <- struct{}{}:
	default:
	}
	writeJSON(w, 202, map[string]string{"status": "restarting", "revision": s.revision})
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		jsonError(w, 405, "GET erforderlich.")
		return
	}
	s.mu.RLock()
	settings := s.settings
	s.mu.RUnlock()
	w.Header().Set("Content-Disposition", `attachment; filename="reolink-sip-config.json"`)
	writeJSON(w, 200, settings)
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) error {
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func jsonError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func (s *Server) Serve(ctx context.Context, address string) error {
	if s.Run == nil {
		return errors.New("missing gateway runner")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	managed := make(chan struct{})
	go func() { defer close(managed); s.Manage(ctx) }()
	server := &http.Server{Addr: address, Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
	}()
	if s.Logger != nil {
		s.Logger.Info("standalone setup ready", "address", address, "admin_token_file", filepath.Join(s.DataDir, "admin-token"))
	}
	err := server.ListenAndServe()
	cancel()
	<-stopped
	<-managed
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("standalone web server: %w", err)
}
