package status

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/audiostats"
)

type testCommands struct {
	testCall func(context.Context, string) error
	hangup   func(context.Context) error
}

const testInstanceID = "12345678-1234-5678-9234-567812345678"

func (c testCommands) StartTestCall(ctx context.Context, routeID string) error {
	if c.testCall == nil {
		return nil
	}
	return c.testCall(ctx, routeID)
}

func (c testCommands) Hangup(ctx context.Context) error {
	if c.hangup == nil {
		return nil
	}
	return c.hangup(ctx)
}

func TestAPIV1RequiresBearerToken(t *testing.T) {
	store, handler := newTestAPI(t, testCommands{})
	_ = store
	for _, token := range []string{"", "wrong"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
		req.RemoteAddr = "192.168.1.20:1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status = %d, want 401", token, res.Code)
		}
	}

	req := authenticatedRequest(http.MethodGet, "/api/v1/info")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("valid token status = %d body=%s", res.Code, res.Body.String())
	}
	var info APIInfo
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.APIVersion != APIVersion || info.GatewayVersion != "1.5.0" || info.InstanceID != testInstanceID {
		t.Fatalf("unexpected info: %#v", info)
	}
	if !strings.Contains(strings.Join(info.Capabilities, ","), "dtmf_events") {
		t.Fatalf("DTMF capability is missing: %#v", info.Capabilities)
	}
	if !strings.Contains(strings.Join(info.Capabilities, ","), "parallel_calls") {
		t.Fatalf("parallel-call capability is missing: %#v", info.Capabilities)
	}
	if !strings.Contains(strings.Join(info.Capabilities, ","), "route_test_calls") {
		t.Fatalf("route test-call capability is missing: %#v", info.Capabilities)
	}
}

func TestAPIV1RejectsPublicRemoteEvenWithToken(t *testing.T) {
	_, handler := newTestAPI(t, testCommands{})
	req := authenticatedRequest(http.MethodGet, "/api/v1/status")
	req.RemoteAddr = "203.0.113.10:1234"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
}

func TestAPIV1StatusMapping(t *testing.T) {
	store, handler := newTestAPI(t, testCommands{})
	started := time.Now().Add(-time.Minute).Round(time.Second)
	store.Update(func(snapshot *Snapshot) {
		snapshot.State = "active"
		snapshot.DoorCallEnabled = true
		snapshot.SIPRegistered = true
		snapshot.ParallelCallEnabled = true
		snapshot.ParallelSIPRegistered = true
		snapshot.CurrentCallDirection = "incoming"
		snapshot.LastCallDirection = "incoming"
		snapshot.CurrentCallerNumber = "01631416518"
		snapshot.LastCallerNumber = "01631416518"
		snapshot.CurrentRouteID = "wohnung_1"
		snapshot.CurrentRouteName = "Wohnung 1"
		snapshot.LastRouteID = "wohnung_1"
		snapshot.LastRouteName = "Wohnung 1"
		snapshot.LastCallStarted = started
		snapshot.ActiveCodec = "pcma"
	})
	req := authenticatedRequest(http.MethodGet, "/api/v1/status")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var status APIStatus
	if err := json.Unmarshal(res.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Call.Active || status.Call.State != "active" || status.Call.CallerNumber != "01631416518" || !status.Controls.HangupAvailable {
		t.Fatalf("unexpected status mapping: %#v", status)
	}
	if status.Call.RouteID != "wohnung_1" || status.Call.LastRouteName != "Wohnung 1" {
		t.Fatalf("unexpected route status: %#v", status.Call)
	}
	if status.Controls.TestCallAvailable {
		t.Fatal("test call must not be available during a call")
	}
	if !status.SIP.Registered || !status.SIP.DoorCallEnabled || !status.SIP.DoorRegistered || !status.SIP.ParallelCallEnabled || !status.SIP.ParallelRegistered {
		t.Fatalf("unexpected SIP account status: %#v", status.SIP)
	}
}

func TestAPIV1RouteAvailabilityUsesEachRoutesAccounts(t *testing.T) {
	snapshot := Snapshot{
		Version: "1.5.0", State: "idle", DoorCallEnabled: true, SIPRegistered: true,
		ParallelCallEnabled: true, ParallelSIPRegistered: false,
	}
	routes := []RouteDefinition{
		{ID: "door", Name: "Tür", DoorCall: true},
		{ID: "mobile", Name: "Mobil", MobileCall: true},
		{ID: "combined", Name: "Kombiniert", DoorCall: true, MobileCall: true},
	}
	status := newAPIStatus(snapshot, routes)
	if len(status.Routes) != 3 || !status.Routes[0].TestCallAvailable || status.Routes[1].TestCallAvailable || !status.Routes[2].TestCallAvailable {
		t.Fatalf("unexpected route availability: %#v", status.Routes)
	}
}

func TestAPIV1UnknownRouteCommandReturnsNotFound(t *testing.T) {
	_, handler := newTestAPI(t, testCommands{
		testCall: func(context.Context, string) error { return ErrRouteNotFound },
	})
	req := authenticatedRequest(http.MethodPost, "/api/v1/routes/missing/test")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound || !strings.Contains(res.Body.String(), "route_not_found") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAPIV1MobileOnlyRegistrationIsAvailable(t *testing.T) {
	status := newAPIStatus(Snapshot{
		Version: "1.5.0", State: "idle", ParallelCallEnabled: true, ParallelSIPRegistered: true,
	}, nil)
	if !status.SIP.Registered || status.SIP.DoorCallEnabled || status.SIP.DoorRegistered || !status.SIP.ParallelRegistered {
		t.Fatalf("unexpected mobile-only SIP status: %#v", status.SIP)
	}
	if !status.Controls.TestCallAvailable {
		t.Fatalf("mobile-only test call should be available: %#v", status.Controls)
	}
}

func TestAPIV1Commands(t *testing.T) {
	testCalled := false
	testRoute := ""
	hangupCalled := false
	_, handler := newTestAPI(t, testCommands{
		testCall: func(_ context.Context, routeID string) error { testCalled = true; testRoute = routeID; return nil },
		hangup:   func(context.Context) error { hangupCalled = true; return nil },
	})
	for _, path := range []string{"/api/v1/calls/test", "/api/v1/calls/hangup"} {
		req := authenticatedRequest(http.MethodPost, path)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d body=%s", path, res.Code, res.Body.String())
		}
	}
	if !testCalled || !hangupCalled {
		t.Fatalf("callbacks test=%t hangup=%t", testCalled, hangupCalled)
	}
	req := authenticatedRequest(http.MethodPost, "/api/v1/routes/wohnung_1/test")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted || testRoute != "wohnung_1" {
		t.Fatalf("route test call status=%d route=%q body=%s", res.Code, testRoute, res.Body.String())
	}
}

func TestAPIV1CommandErrors(t *testing.T) {
	_, handler := newTestAPI(t, testCommands{
		testCall: func(context.Context, string) error { return ErrCallBusy },
		hangup:   func(context.Context) error { return ErrNoActiveCall },
	})
	tests := []struct {
		path string
		want int
	}{
		{"/api/v1/calls/test", http.StatusConflict},
		{"/api/v1/calls/hangup", http.StatusNoContent},
	}
	for _, tt := range tests {
		req := authenticatedRequest(http.MethodPost, tt.path)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tt.want {
			t.Fatalf("%s status = %d, want %d body=%s", tt.path, res.Code, tt.want, res.Body.String())
		}
	}
}

func TestAPIV1EventsStartsWithCompleteSnapshot(t *testing.T) {
	store, handler := newTestAPI(t, testCommands{})
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected event response: %s content-type=%q", response.Status, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	var data string
	for data == "" {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
	var status APIStatus
	if err := json.Unmarshal([]byte(data), &status); err != nil {
		t.Fatal(err)
	}
	if status.Revision != store.Get().Revision || status.APIVersion != APIVersion {
		t.Fatalf("unexpected event status: %#v", status)
	}
}

func TestAPIV1EventsPublishesTransientDTMFWithoutChangingRevision(t *testing.T) {
	store, handler := newTestAPI(t, testCommands{})
	store.Update(func(snapshot *Snapshot) {
		snapshot.State = "active"
		snapshot.CurrentCallDirection = "incoming"
		snapshot.CurrentCallerNumber = "**620"
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first := readSSEEvent(t, reader)
	if first["event"] != "status" || first["id"] == "" {
		t.Fatalf("unexpected initial event: %#v", first)
	}

	revision := store.Get().Revision
	receivedAt := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	store.PublishDTMF("#", 120, receivedAt, "incoming", "**620", "call-123@example.org")
	event := readSSEEvent(t, reader)
	if event["event"] != "dtmf" || event["id"] != "" {
		t.Fatalf("unexpected DTMF SSE envelope: %#v", event)
	}
	var payload APIDTMFEvent
	if err := json.Unmarshal([]byte(event["data"]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.APIVersion != APIVersion || payload.Digit != "#" || payload.DurationMS != 120 ||
		payload.CallDirection != "incoming" || payload.RemoteNumber != "**620" ||
		payload.CallID != "call-123@example.org" ||
		payload.InstanceID != testInstanceID || !payload.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("unexpected DTMF payload: %#v", payload)
	}
	if store.Get().Revision != revision {
		t.Fatalf("DTMF changed status revision: got %d want %d", store.Get().Revision, revision)
	}
}

func readSSEEvent(t *testing.T, reader *bufio.Reader) map[string]string {
	t.Helper()
	event := make(map[string]string)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" && len(event) > 0 {
			return event
		}
		field, value, found := strings.Cut(line, ":")
		if !found || field == "" {
			continue
		}
		event[field] = strings.TrimPrefix(value, " ")
	}
}

func TestWriteCommandErrorDoesNotExposeInternalDetails(t *testing.T) {
	res := httptest.NewRecorder()
	writeCommandError(res, errors.New("password=secret"))
	if res.Code != http.StatusInternalServerError || strings.Contains(res.Body.String(), "secret") {
		t.Fatalf("unsafe error response: status=%d body=%s", res.Code, res.Body.String())
	}
}

func newTestAPI(t *testing.T, commands CommandHandler) (*Store, http.Handler) {
	t.Helper()
	store := New("1.5.0")
	store.SetRoutes([]RouteDefinition{{ID: "default", Name: "Standard", DoorCall: true}})
	mux := http.NewServeMux()
	store.registerAPIRoutes(mux, ServerOptions{Token: "test-token", InstanceID: testInstanceID, Commands: commands})
	return store, mux
}

func authenticatedRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.168.1.20:1234"
	req.Header.Set("Authorization", "Bearer test-token")
	return req
}

func TestAPICameraAudioDiagnosticsSurviveHangup(t *testing.T) {
	store, handler := newTestAPI(t, testCommands{})
	counts := audiostats.Snapshot{Available: true, Packets: 100, EncodedBytes: 2048, PCMSamples: 8000, PCMPeak: 8192, RTPPackets: 50}
	store.Update(func(s *Snapshot) { s.State = "active"; s.CameraAudio = counts })
	store.Update(func(s *Snapshot) { s.State = "idle"; s.ActiveReceive = ""; s.ReceiveDetails = "" })
	req := authenticatedRequest(http.MethodGet, "/api/v1/status")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	var result APIStatus
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Call.Active || result.Media.CameraAudio == nil || *result.Media.CameraAudio != counts {
		t.Fatalf("last-call diagnostics lost: %s", res.Body.String())
	}
	if got := newAPIStatus(Snapshot{}, nil).Media.CameraAudio; got != nil {
		t.Fatalf("unexpected counters without Baichuan: %+v", got)
	}
}
