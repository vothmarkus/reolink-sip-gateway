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
	"sort"
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
	DoorCallEnabled               bool      `json:"door_call_enabled"`
	SIPRegistered                 bool      `json:"sip_registered"`
	LastRegistrationErr           string    `json:"last_registration_error,omitempty"`
	ParallelCallEnabled           bool      `json:"parallel_call_enabled"`
	ParallelSIPRegistered         bool      `json:"parallel_sip_registered"`
	LastParallelRegistrationErr   string    `json:"last_parallel_registration_error,omitempty"`
	LastVisitorEvent              time.Time `json:"last_visitor_event,omitempty"`
	LastCallStarted               time.Time `json:"last_call_started,omitempty"`
	LastCallEnded                 time.Time `json:"last_call_ended,omitempty"`
	CurrentCallDirection          string    `json:"current_call_direction,omitempty"`
	LastCallDirection             string    `json:"last_call_direction,omitempty"`
	CurrentCallerNumber           string    `json:"current_caller_number,omitempty"`
	LastCallerNumber              string    `json:"last_caller_number,omitempty"`
	CurrentRouteID                string    `json:"current_route_id,omitempty"`
	CurrentRouteName              string    `json:"current_route_name,omitempty"`
	LastRouteID                   string    `json:"last_route_id,omitempty"`
	LastRouteName                 string    `json:"last_route_name,omitempty"`
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
	FritzFonLiveImageEnabled      bool      `json:"fritzfon_live_image_enabled"`
	ActiveEchoCancellation        string    `json:"active_echo_cancellation,omitempty"`
	ActiveTalkback                string    `json:"active_talkback,omitempty"`
	TalkbackDetails               string    `json:"talkback_details,omitempty"`
	ActiveReceive                 string    `json:"active_receive,omitempty"`
	ReceiveDetails                string    `json:"receive_details,omitempty"`
}

type dtmfEvent struct {
	Digit         string
	DurationMS    int
	ReceivedAt    time.Time
	CallDirection string
	RemoteNumber  string
	CallID        string
}

type subscriber struct {
	status chan Snapshot
	dtmf   chan dtmfEvent
}

type RouteDefinition struct {
	ID         string
	Name       string
	DoorCall   bool
	MobileCall bool
}

type Store struct {
	mu             sync.RWMutex
	value          Snapshot
	subscribers    map[uint64]subscriber
	nextSubscriber uint64
	routes         []RouteDefinition
}

func New(version string) *Store {
	now := time.Now()
	return &Store{
		value:       Snapshot{Version: version, StartedAt: now, UpdatedAt: now, Revision: 1, State: "starting"},
		subscribers: make(map[uint64]subscriber),
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
		case subscriber.status <- current:
		default:
			// A status stream needs the newest complete snapshot, not an
			// unbounded history. Replace one stale buffered update.
			select {
			case <-subscriber.status:
			default:
			}
			select {
			case subscriber.status <- current:
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

func (s *Store) SetRoutes(routes []RouteDefinition) {
	s.mu.Lock()
	s.routes = append([]RouteDefinition(nil), routes...)
	s.mu.Unlock()
}

func (s *Store) Routes() []RouteDefinition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]RouteDefinition(nil), s.routes...)
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
	s.subscribers[id] = subscriber{status: updates}
	s.mu.Unlock()
	return updates, s.cancelSubscriber(id)
}

// subscribeEvents atomically registers both status and transient DTMF streams,
// so an SSE client cannot miss an event in the gap between two subscriptions.
func (s *Store) subscribeEvents() (<-chan Snapshot, <-chan dtmfEvent, func()) {
	s.mu.Lock()
	s.nextSubscriber++
	id := s.nextSubscriber
	updates := make(chan Snapshot, 1)
	dtmf := make(chan dtmfEvent, 64)
	updates <- s.value
	s.subscribers[id] = subscriber{status: updates, dtmf: dtmf}
	s.mu.Unlock()
	return updates, dtmf, s.cancelSubscriber(id)
}

func (s *Store) cancelSubscriber(id uint64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subscribers, id)
			s.mu.Unlock()
		})
	}
}

// PublishDTMF fans one completed keypress out to connected SSE clients without
// mutating the persistent status snapshot or its revision. Call metadata is
// supplied by the owning call lifetime so teardown cannot erase it first.
func (s *Store) PublishDTMF(
	digit string,
	durationMS int,
	receivedAt time.Time,
	callDirection, remoteNumber, callID string,
) {
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event := dtmfEvent{
		Digit: digit, DurationMS: durationMS, ReceivedAt: receivedAt.UTC(),
		CallDirection: callDirection, RemoteNumber: remoteNumber, CallID: callID,
	}
	for _, subscriber := range s.subscribers {
		if subscriber.dtmf == nil {
			continue
		}
		select {
		case subscriber.dtmf <- event:
		default:
			// The bounded per-client queue prevents a stalled SSE reader from
			// applying backpressure to the real-time RTP receive path.
		}
	}
}

func (s *Store) Serve(ctx context.Context, options ServerOptions) error {
	if options.Port < 1 || options.Port > 65535 {
		return fmt.Errorf("invalid status/API port %d", options.Port)
	}
	if !validAPIToken(options.Token) || !validInstanceID(options.InstanceID) {
		return errors.New("integration API identity is invalid")
	}
	if options.LiveImageProvider != nil && !validLiveImageToken(options.LiveImageToken) {
		return errors.New("FRITZ!Fon live image identity is invalid")
	}
	mux := http.NewServeMux()
	s.registerAPIRoutes(mux, options)
	liveImageEnabled := options.LiveImageProvider != nil
	liveImageAddressValue := ""
	if liveImageEnabled {
		liveImageAddressValue = liveImageAddress(options.LiveImageHost, options.Port, options.LiveImageToken)
		mux.Handle(liveImagePath(options.LiveImageToken), localLiveImageOnly(liveImageHandler(options.LiveImageProvider)))
		if provider, ok := options.LiveImageProvider.(MultiChannelJPEGProvider); ok {
			mux.Handle(liveImageChannelPrefix(options.LiveImageToken), localLiveImageOnly(liveImageChannelHandler(provider, options.LiveImageToken)))
		}
	}
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
		_ = page.Execute(w, pageData{
			Snapshot: s.Get(), APIHostname: setupHostname(options.Hostname), APIToken: options.Token,
			LiveImageAvailable: liveImageEnabled, LiveImageAddress: liveImageAddressValue,
			LiveImageURL: fullLiveImageURL(liveImageAddressValue), LiveImageChannels: liveImagePageChannels(options),
			LiveImageDiscovery: liveImagePageDiscoveryData(options),
		})
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
	APIHostname        string
	APIToken           string
	LiveImageAvailable bool
	LiveImageAddress   string
	LiveImageURL       string
	LiveImageChannels  []liveImagePageChannel
	LiveImageDiscovery liveImagePageDiscovery
}

type liveImagePageChannel struct {
	Number         int
	Name           string
	Primary        bool
	StatusText     string
	StatusClass    string
	CaptureText    string
	CaptureClass   string
	Address        string
	URL            string
	ChannelAddress string
	ChannelURL     string
}

type liveImagePageDiscovery struct {
	Available   bool
	StatusText  string
	StatusClass string
	LastAttempt time.Time
	LastSuccess time.Time
}

func liveImagePageChannels(options ServerOptions) []liveImagePageChannel {
	provider, ok := options.LiveImageProvider.(MultiChannelJPEGProvider)
	if !ok {
		return nil
	}
	available := append([]LiveImageChannel(nil), provider.LiveImageChannels()...)
	sort.SliceStable(available, func(i, j int) bool { return available[i].Number < available[j].Number })
	seen := make(map[int]bool, len(available))
	channels := make([]liveImagePageChannel, 0, len(available))
	for _, channel := range available {
		if channel.Number < 1 || channel.Number > 256 || seen[channel.Number] {
			continue
		}
		seen[channel.Number] = true
		channelAddress := liveImageChannelAddress(options.LiveImageHost, options.Port, options.LiveImageToken, channel.Number)
		entry := liveImagePageChannel{
			Number: channel.Number, Name: strings.TrimSpace(channel.Name), Primary: channel.Primary,
			ChannelAddress: channelAddress, ChannelURL: fullLiveImageURL(channelAddress),
			StatusText: liveImageChannelStatusText(channel), StatusClass: liveImageChannelStatusClass(channel),
			CaptureText: liveImageCaptureText(channel), CaptureClass: liveImageCaptureClass(channel),
		}
		if validLiveImageCameraID(channel.CameraID) {
			entry.Address = liveImageCameraAddress(options.LiveImageHost, options.Port, options.LiveImageToken, channel.CameraID)
			entry.URL = fullLiveImageURL(entry.Address)
		}
		channels = append(channels, entry)
	}
	return channels
}

func liveImagePageDiscoveryData(options ServerOptions) liveImagePageDiscovery {
	provider, ok := options.LiveImageProvider.(MultiChannelJPEGProvider)
	if !ok {
		return liveImagePageDiscovery{}
	}
	diagnostics := provider.LiveImageDiscovery()
	result := liveImagePageDiscovery{
		Available: true, LastAttempt: diagnostics.LastAttempt, LastSuccess: diagnostics.LastSuccess,
		StatusText: "noch nicht ausgeführt", StatusClass: "muted",
	}
	if diagnostics.LastFailed {
		result.StatusText = "vorübergehend nicht erreichbar – letzter Katalog bleibt aktiv"
		result.StatusClass = "bad"
	} else if !diagnostics.LastAttempt.IsZero() {
		result.StatusText = "erfolgreich"
		result.StatusClass = "ok"
	} else if !diagnostics.LastSuccess.IsZero() {
		result.StatusText = "gespeicherter Katalog geladen; neue Prüfung läuft"
	}
	return result
}

func liveImageChannelStatusText(channel LiveImageChannel) string {
	if !channel.StatusKnown {
		return "noch nicht neu geprüft"
	}
	if channel.Online {
		return "online"
	}
	return "offline"
}

func liveImageChannelStatusClass(channel LiveImageChannel) string {
	if !channel.StatusKnown {
		return "muted"
	}
	if channel.Online {
		return "ok"
	}
	return "bad"
}

func liveImageCaptureText(channel LiveImageChannel) string {
	if channel.LastImageAttempt.IsZero() {
		return "noch kein Bild abgerufen"
	}
	if channel.LastImageFailed {
		text := "letzter Abruf fehlgeschlagen am " + formatTime(channel.LastImageAttempt)
		if !channel.LastImageSuccess.IsZero() {
			text += "; letzter Erfolg " + formatTime(channel.LastImageSuccess)
			if channel.LastImageSource != "" {
				text += " über " + channel.LastImageSource
			}
		}
		if channel.LastImageDuration > 0 {
			text += fmt.Sprintf(" (Fehlversuch %d ms)", channel.LastImageDuration.Round(time.Millisecond).Milliseconds())
		}
		return text
	}
	text := "erfolgreich am " + formatTime(channel.LastImageSuccess)
	if channel.LastImageSource != "" {
		text += " über " + channel.LastImageSource
	}
	if channel.LastImageDuration > 0 {
		text += fmt.Sprintf(" (%d ms)", channel.LastImageDuration.Round(time.Millisecond).Milliseconds())
	}
	return text
}

func liveImageCaptureClass(channel LiveImageChannel) string {
	if channel.LastImageAttempt.IsZero() {
		return "muted"
	}
	if channel.LastImageFailed {
		return "bad"
	}
	return "ok"
}

func setupHostname(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "_", "-")
}

//go:embed page.html
var statusPageHTML string

var page = template.Must(template.New("status").Funcs(template.FuncMap{"time": formatTime}).Parse(statusPageHTML))
