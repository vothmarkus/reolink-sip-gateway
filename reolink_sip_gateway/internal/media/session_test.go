package media

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/vothmarkus/reolink-sip-gateway/internal/config"
	"github.com/vothmarkus/reolink-sip-gateway/internal/g711"
	"github.com/vothmarkus/reolink-sip-gateway/internal/rtp"
	"github.com/vothmarkus/reolink-sip-gateway/internal/sip"
)

func TestRedactRemovesEncodedRTSPCredentials(t *testing.T) {
	s := &Session{cfg: config.Config{ReolinkPassword: "p@ss word"}}
	got := s.redact("failed rtsp://user:p%40ss%20word@192.0.2.1/stream: unauthorized")
	if strings.Contains(got, "user:") || strings.Contains(got, "p%40ss") || strings.Contains(got, "p@ss") {
		t.Fatalf("credentials leaked: %q", got)
	}
}

func TestBuildFFmpegArgsUsesRTSPDemuxerTimeout(t *testing.T) {
	args := buildFFmpegArgs("rtsp://example.invalid/stream", 5002, sip.Codec{Name: g711.PCMA, PayloadType: 8})
	for _, arg := range args {
		if arg == "-rw_timeout" {
			t.Fatal("generic -rw_timeout must not be used for RTSP live input")
		}
	}
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-timeout" && args[i+1] == "10000000" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("RTSP -timeout option missing from FFmpeg args: %v", args)
	}
}

func TestBuildFFmpegArgsUsesFixedStableTCPPath(t *testing.T) {
	args := strings.Join(buildFFmpegArgs("rtsp://example.invalid/stream", 5002, sip.Codec{Name: g711.PCMU, PayloadType: 0}), " ")
	for _, want := range []string{"-rtsp_transport tcp", "-fflags nobuffer", "-flags low_delay", "-payload_type 0"} {
		if !strings.Contains(args, want) {
			t.Fatalf("stable RTSP args missing %q: %s", want, args)
		}
	}
	for _, retired := range []string{"-reorder_queue_size", "-max_delay", "-probesize 32768", "-flush_packets 1"} {
		if strings.Contains(args, retired) {
			t.Fatalf("retired experimental RTSP tuning leaked into args: %s", args)
		}
	}
}

type fakePCMSource struct {
	pcm  chan []int16
	done chan error
}

func (f *fakePCMSource) PCM() <-chan []int16 { return f.pcm }
func (f *fakePCMSource) Done() <-chan error  { return f.done }

func TestForwardBaichuanToPhoneProducesPacedG711RTP(t *testing.T) {
	remote, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	out, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	call := &sip.Call{Codec: sip.Codec{Name: g711.PCMA, PayloadType: 8}, RemoteRTP: remote.LocalAddr().(*net.UDPAddr)}
	cfg := config.Defaults()
	s := &Session{cfg: cfg, call: call}
	source := &fakePCMSource{pcm: make(chan []int16, 4), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- s.forwardBaichuanToPhone(ctx, source, out, nil, time.Now()) }()

	pcm := make([]int16, 160*6)
	for i := range pcm {
		pcm[i] = int16((i%200 - 100) * 100)
	}
	source.pcm <- pcm

	buf := make([]byte, 2048)
	var prevSeq uint16
	var prevTS uint32
	for i := 0; i < 5; i++ {
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := remote.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("read RTP %d: %v", i, err)
		}
		pkt, err := rtp.Parse(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		if pkt.PayloadType != 8 || len(pkt.Payload) != 160 {
			t.Fatalf("unexpected RTP packet: PT=%d payload=%d", pkt.PayloadType, len(pkt.Payload))
		}
		if i == 0 {
			if !pkt.Marker {
				t.Fatal("first generated RTP packet should set marker")
			}
		} else {
			if pkt.Sequence != prevSeq+1 || pkt.Timestamp != prevTS+160 || pkt.Marker {
				t.Fatalf("non-contiguous RTP packet %d: seq=%d/%d ts=%d/%d marker=%t", i, pkt.Sequence, prevSeq, pkt.Timestamp, prevTS, pkt.Marker)
			}
		}
		prevSeq, prevTS = pkt.Sequence, pkt.Timestamp
	}
	cancel()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("bridge shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}
}
