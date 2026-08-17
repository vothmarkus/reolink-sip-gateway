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
	UpdatedAt                     time.Time `json:"updated_at"`
	Revision                      uint64    `json:"revision"`
	State                         string    `json:"state"`
	DryRun                        bool      `json:"dry_run"`
	HAConnected                   bool      `json:"ha_connected"`
	SIPRegistered                 bool      `json:"sip_registered"`
	LastRegistrationErr           string    `json:"last_registration_error,omitempty"`
	LastVisitorEvent              time.Time `json:"last_visitor_event,omitempty"`
	LastCallStarted               time.Time `json:"last_call_started,omitempty"`
	LastCallEnded                 time.Time `json:"last_call_ended,omitempty"`
	CurrentCallDirection          string    `json:"current_call_direction,omitempty"`
	LastCallDirection             string    `json:"last_call_direction,omitempty"`
	CurrentCallerNumber           string    `json:"current_caller_number,omitempty"`
	LastCallerNumber              string    `json:"last_caller_number,omitempty"`
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
	mu             sync.RWMutex
	value          Snapshot
	subscribers    map[uint64]chan Snapshot
	nextSubscriber uint64
}

func New(version string) *Store {
	now := time.Now()
	return &Store{
		value:       Snapshot{Version: version, StartedAt: now, UpdatedAt: now, Revision: 1, State: "starting"},
		subscribers: make(map[uint64]chan Snapshot),
	}
}

func (s *Store) Update(fn func(*Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.value
	fn(&s.value)
	if s.value == before {
		return
	}
	s.value.Revision++
	s.value.UpdatedAt = time.Now()
	current := s.value
	for _, subscriber := range s.subscribers {
		select {
		case subscriber <- current:
		default:
			// A status stream needs the newest complete snapshot, not an
			// unbounded history. Replace one stale buffered update.
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- current:
			default:
			}
		}
	}
}

func (s *Store) Get() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value
}

// Subscribe returns an initial snapshot followed by the newest changed
// snapshot. The cancel function is idempotent and does not close the channel,
// avoiding a send/close race with concurrent Update calls.
func (s *Store) Subscribe() (<-chan Snapshot, func()) {
	s.mu.Lock()
	s.nextSubscriber++
	id := s.nextSubscriber
	updates := make(chan Snapshot, 1)
	updates <- s.value
	s.subscribers[id] = updates
	s.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subscribers, id)
			s.mu.Unlock()
		})
	}
	return updates, cancel
}

func (s *Store) Serve(ctx context.Context, options ServerOptions) error {
	if options.Port < 1 || options.Port > 65535 {
		return fmt.Errorf("invalid status/API port %d", options.Port)
	}
	if !validAPIToken(options.Token) || !validInstanceID(options.InstanceID) {
		return errors.New("integration API identity is invalid")
	}
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux, options)
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
		_ = page.Execute(w, pageData{Snapshot: s.Get(), APIPort: options.Port, APIToken: options.Token})
	})))

	srv := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", options.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// SSE responses remain open. All non-streaming handlers return small,
		// bounded responses and ReadHeaderTimeout still protects acceptance.
		WriteTimeout: 0,
		IdleTimeout:  30 * time.Second,
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

type pageData struct {
	Snapshot
	APIPort  int
	APIToken string
}

var page = template.Must(template.New("status").Funcs(template.FuncMap{"time": formatTime}).Parse(`<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Reolink SIP Gateway</title><style>body{font:16px system-ui;margin:2rem;max-width:820px}.brand{display:flex;align-items:center;gap:1rem;margin-bottom:1.25rem}.brand img{width:72px;height:72px;border-radius:16px}.brand h1{margin:0}.brand p{margin:.25rem 0 0;color:#666}.integration{background:#f4f6f8;border-radius:10px;padding:1rem;margin:0 0 1.25rem}.integration h2{font-size:1.1rem;margin:0 0 .65rem}.integration p{margin:.4rem 0}.token{display:flex;gap:.5rem;align-items:center;flex-wrap:wrap}.token code{overflow-wrap:anywhere}.token button{padding:.35rem .65rem}table{border-collapse:collapse;width:100%}td{padding:.55rem;border-bottom:1px solid #ddd;vertical-align:top}td:first-child{font-weight:600;width:40%}.ok{color:#087f23}.bad{color:#b00020}.muted{color:#666}code{background:#eee;padding:.15rem .3rem;border-radius:4px}@media(max-width:520px){body{margin:1rem}.brand img{width:60px;height:60px}.brand h1{font-size:1.55rem}}</style></head><body><div class="brand"><img src="./logo.png" alt="Reolink SIP Gateway"><div><h1>Reolink SIP Gateway</h1><p>Reolink ↔ SIP Zwei-Wege-Audio</p></div></div><section class="integration"><h2>Home-Assistant-Integration</h2><p>API-Adresse: <code>http://&lt;Home-Assistant-IP&gt;:{{.APIPort}}/api/v1</code></p><div class="token"><span>API-Token:</span><code id="api-token">{{.APIToken}}</code><button type="button" onclick="navigator.clipboard.writeText(document.getElementById('api-token').textContent)">kopieren</button></div><p class="muted">Token vertraulich behandeln; er berechtigt zu Testanruf und Auflegen.</p></section><table>
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
<tr><td>Aktuelle Anrufrichtung</td><td>{{if .CurrentCallDirection}}{{.CurrentCallDirection}}{{else}}–{{end}}</td></tr>
<tr><td>Aktuell anrufende Nummer</td><td>{{if .CurrentCallerNumber}}{{.CurrentCallerNumber}}{{else}}–{{end}}</td></tr>
<tr><td>Letzte anrufende Nummer</td><td>{{if .LastCallerNumber}}{{.LastCallerNumber}}{{else}}–{{end}}</td></tr>
<tr><td>Letzte Anrufrichtung</td><td>{{if .LastCallDirection}}{{.LastCallDirection}}{{else}}–{{end}}</td></tr>
<tr><td>Letztes Klingeln</td><td>{{time .LastVisitorEvent}}</td></tr>
<tr><td>Letzter Registrierungsfehler</td><td>{{.LastRegistrationErr}}</td></tr>
<tr><td>Letzter Fehler</td><td>{{.LastError}}</td></tr>
<tr><td>Start</td><td>{{time .StartedAt}}</td></tr>
<tr><td>Version</td><td>{{.Version}}</td></tr>
</table><p>Die Seite aktualisiert sich automatisch.</p><script>setTimeout(()=>location.reload(),5000)</script></body></html>`))
