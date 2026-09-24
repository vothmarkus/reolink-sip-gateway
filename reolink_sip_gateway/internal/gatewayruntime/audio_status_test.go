package gatewayruntime

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/audiostats"
	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/media"
	statuspkg "github.com/vothmarkus/reolink-sip-gateway/internal/status"
)

func TestCameraAudioRetainedAfterHangupAndProtectedFromOldCall(t *testing.T) {
	store := statuspkg.New("test")
	started := time.Now()
	counts := audiostats.Snapshot{Available: true, Packets: 10, PCMSamples: 5120, PCMPeak: 8192, RTPPackets: 30}
	store.Update(func(s *statuspkg.Snapshot) {
		s.LastCallStarted = started
		s.CameraAudio = counts
		s.State = "active"
	})
	finishCall(store, slog.New(slog.NewTextHandler(io.Discard, nil)), started, nil, "test")
	if got := store.Get(); got.State != "idle" || got.CameraAudio != counts {
		t.Fatalf("hangup lost audio counters: %+v", got.CameraAudio)
	}
	oldSession := media.New(config.Defaults().WithResolvedReolinkMode("direct"), nil, nil, nil, nil)
	store.Update(func(s *statuspkg.Snapshot) { s.LastCallStarted = started.Add(time.Second) })
	updateMediaStats(store, oldSession, started)
	if got := store.Get().CameraAudio; got != counts {
		t.Fatalf("previous call overwrote the next call: %+v", got)
	}
}

func TestFailedDialKeepsPreviousAudioAndEchoDiagnostics(t *testing.T) {
	store := statuspkg.New("test")
	old := time.Now().Add(-time.Minute)
	counts := audiostats.Snapshot{Available: true, Packets: 100, PCMSamples: 8000, RTPPackets: 50}
	echo := audiostats.EchoSnapshot{Available: true, CaptureFrames: 100, ERLEValid: true, ERLEDB: 15}
	store.Update(func(s *statuspkg.Snapshot) {
		s.LastCallStarted, s.MediaCallStarted = old, old
		s.CameraAudio, s.EchoStats = counts, echo
	})
	cfg := config.Defaults().WithResolvedReolinkMode("direct")
	cfg.DryRun = false
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// No registered leg: no media session can start, just as with an unanswered
	// outgoing INVITE. The actual call handler must retain the preceding values.
	handleCall(context.Background(), cfg, config.ResolvedCallRoute{}, nil, nil, store, logger)
	got := store.Get()
	if got.State != "error" || got.CameraAudio != counts || got.EchoStats != echo || !got.MediaCallStarted.Equal(old) || got.LastCallStarted.Equal(old) {
		t.Fatalf("failed dialing destroyed/misattributed audio: %+v", got)
	}
	beginMediaStats(store, cfg, got.LastCallStarted)
	got = store.Get()
	if got.CameraAudio.Packets != 0 || got.EchoStats.Available || !got.MediaCallStarted.Equal(got.LastCallStarted) {
		t.Fatal("new media session did not reset counters")
	}
}
