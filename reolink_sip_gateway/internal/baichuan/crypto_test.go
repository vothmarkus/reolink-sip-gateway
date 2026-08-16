package baichuan

import (
	"bytes"
	"testing"
)

func TestMD5ModernKnownVector(t *testing.T) {
	if got, want := MD5Modern("admin"), "21232F297A57A5A743894A0E4A801FC"; got != want {
		t.Fatalf("MD5Modern()=%q want=%q", got, want)
	}
}

func TestBCXORRoundTrip(t *testing.T) {
	plain := []byte(`<?xml version="1.0"?><body><nonce>abc</nonce></body>`)
	encrypted := BCXOR(7, plain)
	decrypted := BCXOR(7, encrypted)
	if !bytes.Equal(decrypted, plain) {
		t.Fatalf("BCXOR roundtrip mismatch")
	}
}

func TestAESCFBRoundTrip(t *testing.T) {
	key := DeriveAESKey("nonce", "password")
	plain := []byte(`<?xml version="1.0"?><body><TalkAbility/></body>`)
	enc := aesCFB(plain, key, true)
	dec := aesCFB(enc, key, false)
	if !bytes.Equal(dec, plain) {
		t.Fatalf("AES-CFB roundtrip mismatch")
	}
}
