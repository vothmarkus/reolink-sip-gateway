package liveimage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
)

func TestCatalogDiscoversOnlineNVRChannelsAndFetchesEachJPEG(t *testing.T) {
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
					{"channel":0,"name":"  Einfahrt   links  ","online":1},
					{"channel":1,"name":"Video Doorbell","online":1},
					{"channel":2,"name":"Garage","online":0},
					{"channel":300,"name":"invalid","online":1}
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

	channels, err := catalog.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Channel{{Number: 1, Name: "Einfahrt links"}, {Number: 2, Name: "Video Doorbell"}}
	if !reflect.DeepEqual(channels, want) || !reflect.DeepEqual(catalog.ChannelNumbers(), []int{1, 2}) {
		t.Fatalf("channels=%#v numbers=%v", channels, catalog.ChannelNumbers())
	}
	if catalog.ChannelName(1) != "Einfahrt links" || catalog.ChannelName(2) != "Video Doorbell" {
		t.Fatalf("channel names: 1=%q 2=%q", catalog.ChannelName(1), catalog.ChannelName(2))
	}

	if _, err := catalog.FetchChannelJPEG(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.FetchJPEG(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sort.Strings(snapshotChannels)
	gotSnapshots := append([]string(nil), snapshotChannels...)
	mu.Unlock()
	if !reflect.DeepEqual(gotSnapshots, []string{"0", "1"}) {
		t.Fatalf("snapshot channels=%v", gotSnapshots)
	}
	if _, err := catalog.FetchChannelJPEG(context.Background(), 3); err == nil {
		t.Fatal("offline channel unexpectedly remained available")
	}
}

func TestCatalogDiscoveryFailureKeepsConfiguredChannelAndHidesPassword(t *testing.T) {
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
	if !reflect.DeepEqual(channels, []Channel{{Number: 4}}) || !reflect.DeepEqual(catalog.ChannelNumbers(), []int{4}) {
		t.Fatalf("fallback channels=%#v", channels)
	}
}

func TestCatalogSuccessfulDiscoveryRetainsConfiguredOfflinePrimary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"cmd":"GetChannelstatus","code":0,"value":{"status":[{"channel":0,"name":"Einfahrt","online":1}]}}]`)
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
	want := []Channel{{Number: 1, Name: "Einfahrt"}, {Number: 4}}
	if !reflect.DeepEqual(channels, want) {
		t.Fatalf("channels=%#v want=%#v", channels, want)
	}
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
