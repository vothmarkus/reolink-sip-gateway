package gatewayruntime

import (
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
	updateCameraAudio(store, oldSession, started)
	if got := store.Get().CameraAudio; got != counts {
		t.Fatalf("previous call overwrote the next call: %+v", got)
	}
}
