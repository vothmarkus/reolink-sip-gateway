package status

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const APIVersion = 1

var (
	ErrCallBusy         = errors.New("another call is active")
	ErrSIPUnavailable   = errors.New("SIP is unavailable")
	ErrNoActiveCall     = errors.New("no call is active")
	ErrCommandUnavailable = errors.New("gateway commands are not ready")
)

var apiCapabilities = []string{
	"call_status",
	"caller_number",
	"events",
	"hangup",
	"test_call",
}

type CommandHandler interface {
	StartTestCall(context.Context) error
	Hangup(context.Context) error
}

type ServerOptions struct {
	Port       int
	Token      string
	InstanceID string
	Commands   CommandHandler
}

type APIInfo struct {
	APIVersion    int      `json:"api_version"`
	GatewayVersion string   `json:"gateway_version"`
	InstanceID    string   `json:"instance_id"`
	Name          string   `json:"name"`
	Capabilities  []string `json:"capabilities"`
}

type APIStatus struct {
	APIVersion int              `json:"api_version"`
	Revision   uint64           `json:"revision"`
	UpdatedAt  time.Time        `json:"updated_at"`
	Gateway    APIGatewayStatus `json:"gateway"`
	SIP        APISIPStatus     `json:"sip"`
	Call       APICallStatus    `json:"call"`
	Media      APIMediaStatus   `json:"media"`
	Controls   APIControls      `json:"controls"`
}

type APIGatewayStatus struct {
	Version                string     `json:"version"`
	State                  string     `json:"state"`
	StartedAt              time.Time  `json:"started_at"`
	HomeAssistantConnected bool       `json:"home_assistant_connected"`
	DryRun                 bool       `json:"dry_run"`
	LastVisitorEvent       *time.Time `json:"last_visitor_event,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
}

type APISIPStatus struct {
	Registered            bool   `json:"registered"`
	LastRegistrationError string `json:"last_registration_error,omitempty"`
}

type APICallStatus struct {
	Active           bool       `json:"active"`
	State            string     `json:"state"`
	Direction        string     `json:"direction,omitempty"`
	LastDirection    string     `json:"last_direction,omitempty"`
	CallerNumber     string     `json:"caller_number,omitempty"`
	LastCallerNumber string     `json:"last_caller_number,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	Codec            string     `json:"codec,omitempty"`
}

type APIMediaStatus struct {
	ConfiguredReolinkMode  string     `json:"configured_reolink_mode"`
	ActiveReolinkMode      string     `json:"active_reolink_mode,omitempty"`
	Profile                string     `json:"profile,omitempty"`
	ReceiveMode            string     `json:"receive_mode,omitempty"`
	ReceiveDetails         string     `json:"receive_details,omitempty"`
	TalkbackMode           string     `json:"talkback_mode,omitempty"`
	TalkbackDetails        string     `json:"talkback_details,omitempty"`
	EchoCancellation       string     `json:"echo_cancellation,omitempty"`
	CalibratedDelayMS      int        `json:"calibrated_delay_ms"`
	CurrentDelayMS         int        `json:"current_delay_ms"`
	CalibrationStatus      string     `json:"calibration_status,omitempty"`
	LastCalibration        *time.Time `json:"last_calibration,omitempty"`
}

type APIControls struct {
	TestCallAvailable bool `json:"test_call_available"`
	HangupAvailable   bool `json:"hangup_available"`
}

type apiCommandResponse struct {
	Status string `json:"status"`
}

type apiErrorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Store) registerAPIRoutes(mux *http.ServeMux, options ServerOptions) {
	auth := func(handler http.HandlerFunc) http.Handler {
		return apiAuthenticated(options.Token, handler)
	}
	mux.Handle("/api/v1/info", auth(func(w http.ResponseWriter, r *http.Request) {
		if !apiMethod(w, r, http.MethodGet) {
			return
		}
		writeAPIJSON(w, http.StatusOK, APIInfo{
			APIVersion: APIVersion, GatewayVersion: s.Get().Version,
			InstanceID: options.InstanceID, Name: "Reolink SIP Gateway",
			Capabilities: append([]string(nil), apiCapabilities...),
		})
	}))
	mux.Handle("/api/v1/status", auth(func(w http.ResponseWriter, r *http.Request) {
		if !apiMethod(w, r, http.MethodGet) {
			return
		}
		writeAPIJSON(w, http.StatusOK, newAPIStatus(s.Get()))
	}))
	mux.Handle("/api/v1/events", auth(func(w http.ResponseWriter, r *http.Request) {
		if !apiMethod(w, r, http.MethodGet) {
			return
		}
		s.serveEvents(w, r)
	}))
	mux.Handle("/api/v1/calls/test", auth(func(w http.ResponseWriter, r *http.Request) {
		if !apiMethod(w, r, http.MethodPost) {
			return
		}
		if options.Commands == nil {
			writeCommandError(w, ErrCommandUnavailable)
			return
		}
		if err := options.Commands.StartTestCall(r.Context()); err != nil {
			writeCommandError(w, err)
			return
		}
		writeAPIJSON(w, http.StatusAccepted, apiCommandResponse{Status: "accepted"})
	}))
	mux.Handle("/api/v1/calls/hangup", auth(func(w http.ResponseWriter, r *http.Request) {
		if !apiMethod(w, r, http.MethodPost) {
			return
		}
		if options.Commands == nil {
			writeCommandError(w, ErrCommandUnavailable)
			return
		}
		if err := options.Commands.Hangup(r.Context()); err != nil {
			if errors.Is(err, ErrNoActiveCall) {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeCommandError(w, err)
			return
		}
		writeAPIJSON(w, http.StatusAccepted, apiCommandResponse{Status: "accepted"})
	}))
	mux.Handle("/api/v1/", auth(func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "API endpoint not found")
	}))
}

func newAPIStatus(snapshot Snapshot) APIStatus {
	active := snapshot.CurrentCallDirection != ""
	callCanStart := snapshot.State == "idle" || snapshot.State == "error"
	callState := "idle"
	if active {
		callState = snapshot.State
	}
	return APIStatus{
		APIVersion: APIVersion,
		Revision:   snapshot.Revision,
		UpdatedAt:  snapshot.UpdatedAt,
		Gateway: APIGatewayStatus{
			Version: snapshot.Version, State: snapshot.State, StartedAt: snapshot.StartedAt,
			HomeAssistantConnected: snapshot.HAConnected, DryRun: snapshot.DryRun,
			LastVisitorEvent: timePointer(snapshot.LastVisitorEvent), LastError: snapshot.LastError,
		},
		SIP: APISIPStatus{Registered: snapshot.SIPRegistered, LastRegistrationError: snapshot.LastRegistrationErr},
		Call: APICallStatus{
			Active: active, State: callState, Direction: snapshot.CurrentCallDirection,
			LastDirection: snapshot.LastCallDirection, CallerNumber: snapshot.CurrentCallerNumber,
			LastCallerNumber: snapshot.LastCallerNumber, StartedAt: timePointer(snapshot.LastCallStarted),
			EndedAt: timePointer(snapshot.LastCallEnded), Codec: snapshot.ActiveCodec,
		},
		Media: APIMediaStatus{
			ConfiguredReolinkMode: snapshot.ConfiguredReolinkMode, ActiveReolinkMode: snapshot.ActiveReolinkMode,
			Profile: snapshot.MediaProfile, ReceiveMode: snapshot.ActiveReceive, ReceiveDetails: snapshot.ReceiveDetails,
			TalkbackMode: snapshot.ActiveTalkback, TalkbackDetails: snapshot.TalkbackDetails,
			EchoCancellation: snapshot.ActiveEchoCancellation, CalibratedDelayMS: snapshot.CalibratedDelayMS,
			CurrentDelayMS: snapshot.CurrentDelayMS, CalibrationStatus: snapshot.CalibrationStatus,
			LastCalibration: timePointer(snapshot.LastCalibration),
		},
		Controls: APIControls{
			TestCallAvailable: !snapshot.DryRun && snapshot.SIPRegistered && !active && callCanStart,
			HangupAvailable:   active,
		},
	}
}

func (s *Store) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "streaming_unavailable", "event streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	updates, unsubscribe := s.Subscribe()
	defer unsubscribe()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case snapshot := <-updates:
			payload, err := json.Marshal(newAPIStatus(snapshot))
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: status\ndata: %s\n\n", snapshot.Revision, payload); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func apiAuthenticated(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedAPIRemote(r.RemoteAddr) {
			writeAPIError(w, http.StatusForbidden, "forbidden", "API access is limited to local networks")
			return
		}
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || token == "" ||
			subtle.ConstantTimeCompare([]byte(fields[1]), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="Reolink SIP Gateway"`)
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedAPIRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func apiMethod(w http.ResponseWriter, r *http.Request, expected string) bool {
	if r.Method == expected {
		return true
	}
	w.Header().Set("Allow", expected)
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

func writeCommandError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCallBusy):
		writeAPIError(w, http.StatusConflict, "call_busy", "another call is active")
	case errors.Is(err, ErrSIPUnavailable):
		writeAPIError(w, http.StatusServiceUnavailable, "sip_unavailable", "SIP is not registered")
	case errors.Is(err, ErrCommandUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeAPIError(w, http.StatusServiceUnavailable, "gateway_unavailable", "gateway commands are not ready")
	default:
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "the command could not be completed")
	}
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeAPIJSON(w, status, apiErrorEnvelope{Error: apiError{Code: code, Message: message}})
}

func writeAPIJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}
