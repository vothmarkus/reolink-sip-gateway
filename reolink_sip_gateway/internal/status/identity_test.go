package status

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateIdentityIsStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	if !validInstanceID(first.InstanceID) || !validAPIToken(first.Token) {
		t.Fatalf("invalid generated identity: %#v", first)
	}
	second, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("reload identity: %v", err)
	}
	if second != first {
		t.Fatalf("identity changed after reload: first=%#v second=%#v", first, second)
	}
	for _, name := range []string{instanceIDFile, apiTokenFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s permissions = %o, want 600", name, got)
		}
	}
}

func TestLoadOrCreateIdentityRejectsInvalidStoredToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, instanceIDFile), []byte("450e8400-e29b-41d4-a716-446655440000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, apiTokenFile), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(dir); err == nil {
		t.Fatal("invalid stored token should fail closed")
	}
}
