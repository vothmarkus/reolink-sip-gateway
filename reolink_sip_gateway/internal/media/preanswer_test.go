package media

import (
	"net"
	"testing"
	"time"
)

func TestDiscardQueuedSIPRTPRemovesBacklogButLeavesSocketUsable(t *testing.T) {
	gateway, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	addr := gateway.LocalAddr().(*net.UDPAddr)
	for i := 0; i < 200; i++ {
		if _, err := sender.WriteToUDP([]byte{byte(i)}, addr); err != nil {
			t.Fatal(err)
		}
	}
	// Give the kernel receive queue a moment to collect the synthetic early media.
	time.Sleep(10 * time.Millisecond)
	discarded, capped, err := discardQueuedSIPRTP(gateway)
	if err != nil {
		t.Fatal(err)
	}
	if capped {
		t.Fatal("unexpected safety-limit hit")
	}
	if discarded != 200 {
		t.Fatalf("discarded=%d, want 200", discarded)
	}

	// The helper must reset its read deadline and must not poison the live receiver.
	if _, err := sender.WriteToUDP([]byte("live"), addr); err != nil {
		t.Fatal(err)
	}
	if err := gateway.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, _, err := gateway.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("socket unusable after drain: %v", err)
	}
	if string(buf[:n]) != "live" {
		t.Fatalf("got %q, want live", string(buf[:n]))
	}
}
