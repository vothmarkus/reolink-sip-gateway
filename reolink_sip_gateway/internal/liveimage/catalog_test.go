package liveimage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func TestCatalogDiscoversNVRChannelsWithStableIDsAndFetchesEachJPEG(t *testing.T) {
	var mu sync.Mutex
	var snapshotChannels []string
	snapshot := testJPEG(t, 320, 240)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch r.URL.Path {
		case "/api.cgi":
			if query.Get("cmd") != "GetChannelstatus" || query.Get("user") != "camera-user" || query.Get("password") != "camera-secret" {
				t.Errorf("unexpected channel discovery query: %v", query)
				http.Error(w, "unexpected request", http.StatusBadRequest)
				return
			}
			_, _ = fmt.Fprint(w, `[{
				"cmd":"GetChannelstatus","code":0,"value":{"count":8,"status":[
					{"channel":0,"name":"  Einfahrt   links  ","online":1,"uid":"camera-a"},
					{"channel":1,"name":"Video Doorbell","online":1,"uid":"camera-b"},
					{"channel":2,"name":"Garage","online":0,"uid":"camera-c"},
					{"channel":3,"name":"","online":0,"uid":""},
					{"channel":300,"name":"invalid","online":1,"uid":"invalid"}
				]}}]`)
		case "/cgi-bin/api.cgi":
			if query.Get("cmd") != "Snap" {
				http.Error(w, "unexpected snapshot", http.StatusBadRequest)
				return
			}
			mu.Lock()
			snapshotChannels = append(snapshotChannels, query.Get("channel"))
			mu.Unlock()
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(snapshot)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.Defaults()
	cfg.ReolinkUsername = "camera-user"
	cfg.ReolinkPassword = "camera-secret"
	cfg.NVRChannel = 1
	catalog := NewCatalog(cfg, nil)
	catalog.client = server.Client()
	catalog.cgiBases = []string{server.URL}
	configureCatalogTestSources(t, catalog, server)
	fixedNow := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	catalog.now = func() time.Time { return fixedNow }

	channels, err := catalog.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Channel{
		{Number: 1, Name: "Einfahrt links", CameraID: cameraIDForUID("camera-a"), Online: true, StatusKnown: true},
		{Number: 2, Name: "Video Doorbell", CameraID: cameraIDForUID("camera-b"), Online: true, StatusKnown: true, Primary: true},
		{Number: 3, Name: "Garage", CameraID: cameraIDForUID("camera-c"), StatusKnown: true},
	}
	if !reflect.DeepEqual(channels, want) || !reflect.DeepEqual(catalog.ChannelNumbers(), []int{1, 2, 3}) {
		t.Fatalf("channels=%#v numbers=%v", channels, catalog.ChannelNumbers())
	}
	if diagnostics := catalog.DiscoveryDiagnostics(); diagnostics.LastAttempt != fixedNow || diagnostics.LastSuccess != fixedNow || diagnostics.LastFailed {
		t.Fatalf("discovery diagnostics=%#v", diagnostics)
	}

	if _, err := catalog.FetchChannelJPEG(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.FetchJPEG(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.FetchCameraJPEG(context.Background(), cameraIDForUID("camera-c")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sort.Strings(snapshotChannels)
	gotSnapshots := append([]string(nil), snapshotChannels...)
	mu.Unlock()
	if !reflect.DeepEqual(gotSnapshots, []string{"0", "1", "2"}) {
		t.Fatalf("snapshot channels=%v", gotSnapshots)
	}
	if _, err := catalog.FetchChannelJPEG(context.Background(), 4); err == nil {
		t.Fatal("unknown channel unexpectedly became available")
	}
	if _, err := catalog.FetchCameraJPEG(context.Background(), strings.Repeat("0", 32)); err == nil {
		t.Fatal("unknown camera unexpectedly became available")
	}
}

func TestCatalogDiscoveryFailureKeepsLastKnownChannelsAndHidesPassword(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not supported", http.StatusNotFound)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.NVRChannel = 3
	cfg.ReolinkPassword = "very-secret-password"
	catalog := NewCatalog(cfg, nil)
	catalog.client = server.Client()
	catalog.cgiBases = []string{server.URL}

	channels, err := catalog.Discover(context.Background())
	if err == nil {
		t.Fatal("expected channel discovery failure")
	}
	if strings.Contains(err.Error(), cfg.ReolinkPassword) {
		t.Fatalf("password leaked in discovery error: %v", err)
	}
	if len(channels) != 1 || channels[0].Number != 4 || !channels[0].Primary {
		t.Fatalf("fallback channels=%#v", channels)
	}
	if diagnostics := catalog.DiscoveryDiagnostics(); diagnostics.LastAttempt.IsZero() || !diagnostics.LastSuccess.IsZero() || !diagnostics.LastFailed {
		t.Fatalf("failure diagnostics=%#v", diagnostics)
	}
}

func TestCatalogSuccessfulDiscoveryRetainsConfiguredOfflinePrimary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"cmd":"GetChannelstatus","code":0,"value":{"status":[{"channel":0,"name":"Einfahrt","online":1,"uid":"front"}]}}]`)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.NVRChannel = 3
	catalog := NewCatalog(cfg, nil)
	catalog.client = server.Client()
	catalog.cgiBases = []string{server.URL}

	channels, err := catalog.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Channel{
		{Number: 1, Name: "Einfahrt", CameraID: cameraIDForUID("front"), Online: true, StatusKnown: true},
		{Number: 4, StatusKnown: true, Primary: true},
	}
	if !reflect.DeepEqual(channels, want) {
		t.Fatalf("channels=%#v want=%#v", channels, want)
	}
}

func TestPersistentCatalogRestoresLinksAndIgnoresDifferentNVR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"cmd":"GetChannelstatus","code":0,"value":{"status":[{"channel":0,"name":"Door","online":1,"uid":"stable-camera-uid"}]}}]`)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.ReolinkHost = "nvr.lan"
	cfg.NVRChannel = 0
	statePath := filepath.Join(t.TempDir(), "catalog.json")
	catalog := NewPersistentCatalog(cfg, nil, statePath)
	catalog.client = server.Client()
	catalog.cgiBases = []string{server.URL}
	discoveredAt := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	catalog.now = func() time.Time { return discoveredAt }
	if _, err := catalog.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog permissions=%o", info.Mode().Perm())
	}
	payload, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainAny(payload, cfg.ReolinkPassword, "camera-secret") {
		t.Fatalf("credentials leaked into state: %s", payload)
	}

	restored := NewPersistentCatalog(cfg, nil, statePath)
	channels := restored.Channels()
	if len(channels) != 1 || channels[0].CameraID != cameraIDForUID("stable-camera-uid") || channels[0].StatusKnown || channels[0].Name != "Door" {
		t.Fatalf("restored channels=%#v", channels)
	}
	if diagnostics := restored.DiscoveryDiagnostics(); diagnostics.LastSuccess != discoveredAt {
		t.Fatalf("restored diagnostics=%#v", diagnostics)
	}

	otherConfig := cfg
	otherConfig.ReolinkHost = "replacement-nvr.lan"
	other := NewPersistentCatalog(otherConfig, nil, statePath)
	otherChannels := other.Channels()
	if len(otherChannels) != 1 || otherChannels[0].CameraID != "" {
		t.Fatalf("foreign state was loaded: %#v", otherChannels)
	}
}

func TestStableCameraIDFollowsChannelMove(t *testing.T) {
	var mu sync.Mutex
	channel := 0
	snapshotChannel := ""
	snapshot := testJPEG(t, 160, 120)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api.cgi" {
			_, _ = fmt.Fprintf(w, `[{"cmd":"GetChannelstatus","code":0,"value":{"status":[{"channel":%d,"name":"Moved camera","online":1,"uid":"same-uid"}]}}]`, channel)
			return
		}
		if r.URL.Path == "/cgi-bin/api.cgi" {
			snapshotChannel = r.URL.Query().Get("channel")
			_, _ = w.Write(snapshot)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	cfg := config.Defaults()
	cfg.NVRChannel = 7
	catalog := NewCatalog(cfg, nil)
	catalog.client = server.Client()
	catalog.cgiBases = []string{server.URL}
	configureCatalogTestSources(t, catalog, server)
	if _, err := catalog.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	cameraID := cameraIDForUID("same-uid")
	mu.Lock()
	channel = 4
	mu.Unlock()
	if _, err := catalog.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.FetchCameraJPEG(context.Background(), cameraID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotSnapshotChannel := snapshotChannel
	mu.Unlock()
	if gotSnapshotChannel != "4" {
		t.Fatalf("stable camera fetched channel %q", gotSnapshotChannel)
	}
	channels := catalog.Channels()
	if len(channels) != 2 || channels[0].Number != 5 || channels[0].CameraID != cameraID || channels[1].Number != 8 || !channels[1].Primary {
		t.Fatalf("moved channels=%#v", channels)
	}
}

func bytesContainAny(value []byte, fragments ...string) bool {
	for _, fragment := range fragments {
		if fragment != "" && strings.Contains(string(value), fragment) {
			return true
		}
	}
	return false
}

func configureCatalogTestSources(t *testing.T, catalog *Catalog, server *httptest.Server) {
	t.Helper()
	configure := func(source *Source) *Source {
		source.client = server.Client()
		source.cgiBases = []string{server.URL}
		source.fallback = func(context.Context) ([]byte, error) {
			return nil, fmt.Errorf("unexpected RTSP fallback")
		}
		return source
	}
	configure(catalog.primary)
	catalog.sourceFactory = func(channel int) *Source {
		if channel == catalog.cfg.NVRChannel {
			return catalog.primary
		}
		return configure(newChannelSource(catalog.cfg, nil, channel))
	}
}
