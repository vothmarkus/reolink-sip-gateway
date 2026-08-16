package status

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed logo.png
var statusLogoPNG []byte

type Snapshot struct {
	Version                       string    `json:"version"`
	StartedAt                     time.Time `json:"started_at"`
	State                         string    `json:"state"`
	HAConnected                   bool      `json:"ha_connected"`
	SIPRegistered                 bool      `json:"sip_registered"`
	LastRegistrationErr           string    `json:"last_registration_error,omitempty"`
	LastVisitorEvent              time.Time `json:"last_visitor_event,omitempty"`
	LastCallStarted               time.Time `json:"last_call_started,omitempty"`
	LastCallEnded                 time.Time `json:"last_call_ended,omitempty"`
	LastError                     string    `json:"last_error,omitempty"`
	ActiveCodec                   string    `json:"active_codec,omitempty"`
	ConfiguredReolinkMode         string    `json:"configured_reolink_mode"`
	ActiveReolinkMode             string    `json:"active_reolink_mode,omitempty"`
	MediaProfile                  string    `json:"media_profile,omitempty"`
	EchoCancellationEnabled       bool      `json:"echo_cancellation_enabled"`
	CalibratedDelayMS             int       `json:"calibrated_delay_ms"`
	CurrentDelayMS                int       `json:"current_delay_ms"`
	AECSearchWindowMS             int       `json:"aec_search_window_ms"`
	AECMinDelayMS                 int       `json:"aec_min_delay_ms"`
	AECMaxDelayMS                 int       `json:"aec_max_delay_ms"`
	CalibrationStatus             string    `json:"calibration_status,omitempty"`
	CalibrationDetails            string    `json:"calibration_details,omitempty"`
	LastCalibration               time.Time `json:"last_calibration,omitempty"`
	WebRTCHighPassFilterEnabled   bool      `json:"webrtc_high_pass_filter_enabled"`
	WebRTCNoiseSuppressionEnabled bool      `json:"webrtc_noise_suppression_enabled"`
	ActiveEchoCancellation        string    `json:"active_echo_cancellation,omitempty"`
	ActiveTalkback                string    `json:"active_talkback,omitempty"`
	TalkbackDetails               string    `json:"talkback_details,omitempty"`
	ActiveReceive                 string    `json:"active_receive,omitempty"`
	ReceiveDetails                string    `json:"receive_details,omitempty"`
}

type Store struct {
	mu    sync.RWMutex
	value Snapshot
}

func New(version string) *Store {
	return &Store{value: Snapshot{Version: version, StartedAt: time.Now(), State: "starting"}}
}
func (s *Store) Update(fn func(*Snapshot)) { s.mu.Lock(); defer s.mu.Unlock(); fn(&s.value) }
func (s *Store) Get() Snapshot             { s.mu.RLock(); defer s.mu.RUnlock(); return s.value }

func (s *Store) Serve(ctx context.Context, port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/logo.png", ingressOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(statusLogoPNG)
	})))
	mux.Handle("/api/status", ingressOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(s.Get())
	})))
	mux.Handle("/", ingressOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'self'")
		_ = page.Execute(w, s.Get())
	})))

	srv := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		<-stopped
		return nil
	}
	return err
}

func ingressOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedRemote(r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	// Home Assistant ingress proxy. The official app documentation states
	// that ingress requests originate from 172.30.32.2.
	return ip.Equal(net.ParseIP("172.30.32.2"))
}

func formatTime(v time.Time) string {
	if v.IsZero() {
		return "–"
	}
	return v.Local().Format("02.01.2006 15:04:05")
}

var page = template.Must(template.New("status").Funcs(template.FuncMap{"time": formatTime}).Parse(`<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Reolink SIP Gateway</title><style>body{font:16px system-ui;margin:2rem;max-width:820px}.brand{display:flex;align-items:center;gap:1rem;margin-bottom:1.25rem}.brand img{width:72px;height:72px;border-radius:16px}.brand h1{margin:0}.brand p{margin:.25rem 0 0;color:#666}table{border-collapse:collapse;width:100%}td{padding:.55rem;border-bottom:1px solid #ddd;vertical-align:top}td:first-child{font-weight:600;width:40%}.ok{color:#087f23}.bad{color:#b00020}.muted{color:#666}code{background:#eee;padding:.15rem .3rem;border-radius:4px}@media(max-width:520px){body{margin:1rem}.brand img{width:60px;height:60px}.brand h1{font-size:1.55rem}}</style></head><body><div class="brand"><img src="./logo.png" alt="Reolink SIP Gateway"><div><h1>Reolink SIP Gateway</h1><p>Reolink ↔ SIP Zwei-Wege-Audio</p></div></div><table>
<tr><td>Status</td><td><code>{{.State}}</code></td></tr>
<tr><td>SIP registriert</td><td>{{if .SIPRegistered}}<span class="ok">ja</span>{{else}}<span class="bad">nein</span>{{end}}</td></tr>
<tr><td>Home Assistant</td><td>{{if .HAConnected}}<span class="ok">verbunden</span>{{else}}<span class="bad">nicht verbunden</span>{{end}}</td></tr>
<tr><td>Konfigurierter Reolink-Modus</td><td>{{.ConfiguredReolinkMode}}</td></tr>
<tr><td>Aktiver Reolink-Modus</td><td>{{if .ActiveReolinkMode}}{{.ActiveReolinkMode}}{{else}}<span class="muted">noch nicht ermittelt</span>{{end}}</td></tr>
<tr><td>Medienweg</td><td>{{if .MediaProfile}}{{.MediaProfile}}{{else}}<span class="muted">noch nicht ermittelt</span>{{end}}</td></tr>
<tr><td>WebRTC AEC</td><td>{{if .EchoCancellationEnabled}}an, Go-Tracking aus (AEC3 intern), Hochpass {{if .WebRTCHighPassFilterEnabled}}an{{else}}aus{{end}}, Rauschfilter {{if .WebRTCNoiseSuppressionEnabled}}moderate{{else}}aus{{end}}{{else}}aus{{end}}</td></tr>
<tr><td>Automatische Kalibrierung</td><td>{{.CalibrationStatus}}{{if .CalibrationDetails}} – {{.CalibrationDetails}}{{end}}</td></tr>
<tr><td>Kalibrierte Latenz</td><td>{{if .EchoCancellationEnabled}}{{.CalibratedDelayMS}} ms{{else}}–{{end}}</td></tr>
<tr><td>Aktuelle Latenz</td><td>{{if .EchoCancellationEnabled}}{{.CurrentDelayMS}} ms{{else}}–{{end}}</td></tr>
<tr><td>Live-Delay-Tracking</td><td>{{if .EchoCancellationEnabled}}aus; kalibrierter Go-Coarse-Delay bleibt während des Calls fest{{else}}–{{end}}</td></tr>
<tr><td>Letzte Kalibrierung</td><td>{{time .LastCalibration}}</td></tr>
<tr><td>Aktiver AEC-Pfad</td><td>{{.ActiveEchoCancellation}}</td></tr>
<tr><td>Aktiver Codec</td><td>{{.ActiveCodec}}</td></tr>
<tr><td>Aktiver Empfang</td><td>{{.ActiveReceive}}{{if .ReceiveDetails}} – {{.ReceiveDetails}}{{end}}</td></tr>
<tr><td>Aktiver Rückkanal</td><td>{{.ActiveTalkback}}{{if .TalkbackDetails}} – {{.TalkbackDetails}}{{end}}</td></tr>
<tr><td>Letztes Klingeln</td><td>{{time .LastVisitorEvent}}</td></tr>
<tr><td>Letzter Registrierungsfehler</td><td>{{.LastRegistrationErr}}</td></tr>
<tr><td>Letzter Fehler</td><td>{{.LastError}}</td></tr>
<tr><td>Start</td><td>{{time .StartedAt}}</td></tr>
<tr><td>Version</td><td>{{.Version}}</td></tr>
</table><p>Die Seite aktualisiert sich automatisch.</p><script>setTimeout(()=>location.reload(),5000)</script></body></html>`))
