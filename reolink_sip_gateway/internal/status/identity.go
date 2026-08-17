package status

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	instanceIDFile = "integration-api-instance-id"
	apiTokenFile   = "integration-api-token"
)

type Identity struct {
	InstanceID string
	Token      string
}

// LoadOrCreateIdentity persists a non-secret instance identifier and a
// 256-bit bearer token in the app data directory. Both survive app upgrades
// and backups, keeping Home Assistant device/entity unique IDs stable.
func LoadOrCreateIdentity(dataDir string) (Identity, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Identity{}, fmt.Errorf("create identity directory: %w", err)
	}
	instanceID, err := loadOrCreateValue(filepath.Join(dataDir, instanceIDFile), newInstanceID, validInstanceID)
	if err != nil {
		return Identity{}, fmt.Errorf("load integration API instance ID: %w", err)
	}
	token, err := loadOrCreateValue(filepath.Join(dataDir, apiTokenFile), newAPIToken, validAPIToken)
	if err != nil {
		return Identity{}, fmt.Errorf("load integration API token: %w", err)
	}
	return Identity{InstanceID: instanceID, Token: token}, nil
}

func loadOrCreateValue(path string, generate func() (string, error), validate func(string) bool) (string, error) {
	value, err := readIdentityValue(path)
	if err == nil {
		if !validate(value) {
			return "", errors.New("stored value is invalid; remove the file explicitly to rotate it")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return "", fmt.Errorf("secure permissions: %w", err)
		}
		return value, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	value, err = generate()
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Be race-safe if two startup paths ever converge here.
		value, err = readIdentityValue(path)
		if err == nil && validate(value) {
			return value, nil
		}
		return "", errors.New("concurrently created value is invalid")
	}
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		_ = f.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.WriteString(value + "\n"); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	complete = true
	return value, nil
}

func readIdentityValue(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func newAPIToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func validAPIToken(value string) bool {
	b, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(b) == 32
}

func newInstanceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// RFC 4122 version 4 / variant 1 bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16])), nil
}

func validInstanceID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(value, "-", "")
	b, err := hex.DecodeString(compact)
	return err == nil && len(b) == 16 && b[6]>>4 == 4 && b[8]>>6 == 2
}
