package media

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
)

type fakeTalkback struct{ mode string }

func (f *fakeTalkback) Run(context.Context, *net.UDPConn, *sip.Call, *audioControls) error {
	return nil
}
func (f *fakeTalkback) PlayPCM(context.Context, []int16, *audioControls) error { return nil }
func (f *fakeTalkback) Close(context.Context) error                            { return nil }
func (f *fakeTalkback) Info() TalkbackInfo                                     { return TalkbackInfo{Mode: f.mode} }

func TestConfiguredTalkbackUsesStartupResolvedProfile(t *testing.T) {
	errRTSP := errors.New("no ONVIF backchannel")
	errNVR := errors.New("basic service unavailable")

	tests := []struct {
		name          string
		configured    string
		resolved      string
		rtspErr       error
		nvrErr        error
		wantMode      string
		wantRTSPCalls int
		wantNVRCalls  int
		wantErr       string
	}{
		{name: "explicit standalone", configured: "standalone", wantMode: "standalone", wantRTSPCalls: 1},
		{name: "explicit nvr", configured: "nvr", wantMode: "nvr", wantNVRCalls: 1},
		{name: "auto resolved standalone", configured: "auto", resolved: "standalone", wantMode: "standalone", wantRTSPCalls: 1},
		{name: "auto resolved nvr", configured: "auto", resolved: "nvr", wantMode: "nvr", wantNVRCalls: 1},
		{name: "unresolved auto rejected", configured: "auto", wantErr: "not resolved during startup"},
		{name: "standalone failure does not fall back", configured: "auto", resolved: "standalone", rtspErr: errRTSP, wantRTSPCalls: 1, wantErr: "standalone ONVIF talkback"},
		{name: "nvr failure does not change profile", configured: "auto", resolved: "nvr", nvrErr: errNVR, wantNVRCalls: 1, wantErr: "NVR talkback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.ReolinkMode = tt.configured
			if tt.resolved != "" {
				cfg = cfg.WithResolvedReolinkMode(tt.resolved)
			}
			rtspCalls, nvrCalls := 0, 0
			rtspOpen := func(context.Context, config.Config, *slog.Logger) (talkbackTransport, error) {
				rtspCalls++
				if tt.rtspErr != nil {
					return nil, tt.rtspErr
				}
				return &fakeTalkback{mode: "standalone"}, nil
			}
			nvrOpen := func(context.Context, config.Config, *slog.Logger) (talkbackTransport, error) {
				nvrCalls++
				if tt.nvrErr != nil {
					return nil, tt.nvrErr
				}
				return &fakeTalkback{mode: "nvr"}, nil
			}

			got, err := openConfiguredTalkbackWith(context.Background(), cfg, nil, rtspOpen, nvrOpen)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got == nil || got.Info().Mode != tt.wantMode {
					t.Fatalf("selected mode=%v, want %q", got, tt.wantMode)
				}
			}
			if rtspCalls != tt.wantRTSPCalls || nvrCalls != tt.wantNVRCalls {
				t.Fatalf("opener calls: RTSP=%d NVR=%d, want %d/%d", rtspCalls, nvrCalls, tt.wantRTSPCalls, tt.wantNVRCalls)
			}
		})
	}
}

func TestValidateBaichuanTalkProfile(t *testing.T) {
	for _, tc := range []struct {
		name      string
		codec     string
		precision int
		rate      int
		samples   int
		wantErr   bool
	}{
		{name: "observed RLN profile", codec: "adpcm", precision: 16, rate: 16000, samples: 1024},
		{name: "precision omitted", codec: "ADPCM", precision: 0, rate: 8000, samples: 160},
		{name: "wrong codec", codec: "aac", precision: 16, rate: 16000, samples: 1024, wantErr: true},
		{name: "odd block", codec: "adpcm", precision: 16, rate: 16000, samples: 1023, wantErr: true},
		{name: "too slow block", codec: "adpcm", precision: 16, rate: 8000, samples: 8000, wantErr: true},
		{name: "too short block", codec: "adpcm", precision: 16, rate: 48000, samples: 2, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBaichuanTalkProfile(tc.codec, tc.precision, tc.rate, tc.samples)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate error=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestPlayPCMBlocksPadsAndWaitsForFinalPlayout(t *testing.T) {
	input := []int16{1, 2, 3, 4, 5}
	var blocks [][]int16
	started := time.Now()
	err := playPCMBlocks(context.Background(), input, 4, 15*time.Millisecond, nil, nil, func(block []int16) error {
		blocks = append(blocks, append([]int16(nil), block...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks=%d want=2", len(blocks))
	}
	if got := blocks[0]; len(got) != 4 || got[0] != 1 || got[1] != 2 || got[2] != 3 || got[3] != 4 {
		t.Fatalf("first block=%v", got)
	}
	if got := blocks[1]; len(got) != 4 || got[0] != 5 || got[1] != 0 || got[2] != 0 || got[3] != 0 {
		t.Fatalf("padded block=%v", got)
	}
	// Two 15 ms blocks must be allowed to play before the incoming call is
	// reported ready and answered. Leave a small margin for timer granularity.
	if elapsed := time.Since(started); elapsed < 25*time.Millisecond {
		t.Fatalf("playout returned too early after %s", elapsed)
	}
}
