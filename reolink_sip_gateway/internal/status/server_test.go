package status

import (
	"bytes"
	"testing"
)

func TestAllowedRemote(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:1234", true},
		{"[::1]:1234", true},
		{"172.30.32.2:4567", true},
		{"192.168.177.10:4567", false},
		{"invalid", false},
	}
	for _, tt := range tests {
		if got := allowedRemote(tt.addr); got != tt.want {
			t.Fatalf("allowedRemote(%q)=%v want %v", tt.addr, got, tt.want)
		}
	}
}

func TestEmbeddedLogoPNG(t *testing.T) {
	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	if len(statusLogoPNG) < len(pngMagic) || !bytes.Equal(statusLogoPNG[:len(pngMagic)], pngMagic) {
		t.Fatal("embedded status logo is not a PNG")
	}
}

func TestStatusPageContainsLogo(t *testing.T) {
	var out bytes.Buffer
	if err := page.Execute(&out, Snapshot{}); err != nil {
		t.Fatalf("render status page: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`src="./logo.png"`)) {
		t.Fatal("status page does not reference embedded logo")
	}
}
