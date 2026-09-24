package media

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/platformaudio"
)

func TestEchoDiagnosticsSurviveCloseWithoutLogger(t *testing.T) {
	fake := &fakePlatformAEC{}
	defer platformaudio.Set(fake)()
	cfg := config.Defaults()
	cfg.SetAECDelay(0)
	ec, err := startEchoCanceller(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ec.Close()
	session := New(cfg, nil, nil, nil, nil)
	session.echo = ec
	if st := session.EchoStats(); !st.Available || st.ERLEValid || st.CaptureFrames != 0 {
		t.Fatalf("warmup presented as measurement: %+v", st)
	}
	frame := make([]int16, aecFrameSamples)
	frame[0] = 1200
	now := time.Now()
	ec.AddRender(frame, now)
	if _, err := ec.ProcessCapture(context.Background(), frame, now); err != nil {
		t.Fatal(err)
	}
	ec.Close()
	st := session.EchoStats()
	if st.CaptureFrames != 1 || st.RenderFrames != 1 || st.MissingRenderFrames != 0 || !st.ERLEValid || st.ERLEDB != 12.5 {
		t.Fatalf("missing real processor counters after close: %+v", st)
	}
}

func TestEchoDiagnosticsRespectNativeValidity(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		st := publicEchoStats(echoStats{CaptureFrames: 1, Native: nativeAECStats{ValidMask: nativeStatERLE | nativeStatResidualEchoLikelihood, EchoReturnLossEnhancementDB: v, ResidualEchoLikelihood: v}})
		if st.ERLEValid || st.ResidualEchoValid {
			t.Fatalf("non-finite metric valid: %+v", st)
		}
		if _, err := json.Marshal(st); err != nil {
			t.Fatal(err)
		}
	}
	st := publicEchoStats(echoStats{CaptureFrames: 1, Native: nativeAECStats{EchoReturnLossEnhancementDB: 12}})
	if st.ERLEValid || st.ERLEDB != 0 {
		t.Fatal("missing validity mask ignored")
	}
}
