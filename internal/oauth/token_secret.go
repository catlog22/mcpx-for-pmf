package oauth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const tokenSecretBytes = 32

// LoadOrCreateTokenSecret returns a stable HMAC key for OAuth access tokens.
// The file is created with exclusive creation and mode 0600 so concurrent
// server starts cannot silently replace an existing signing key.
func LoadOrCreateTokenSecret(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("oauth token secret path required")
	}
	secret, err := readTokenSecret(path)
	if err == nil {
		return secret, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	raw := make([]byte, tokenSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate oauth token secret: %w", err)
	}
	encoded := []byte(hex.EncodeToString(raw) + "\n")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return waitForTokenSecret(path)
		}
		return nil, fmt.Errorf("create oauth token secret: %w", err)
	}
	created := true
	defer func() {
		if created {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write oauth token secret: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync oauth token secret: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close oauth token secret: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure oauth token secret: %w", err)
	}
	created = false
	return raw, nil
}

func waitForTokenSecret(path string) ([]byte, error) {
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		secret, err := readTokenSecret(path)
		if err == nil {
			return secret, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("wait for oauth token secret: %w", lastErr)
}

func readTokenSecret(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, fmt.Errorf("read oauth token secret: %w", err)
	}
	if len(encoded) > 4096 {
		return nil, errors.New("oauth token secret is too large")
	}
	value := strings.TrimSpace(string(encoded))
	if value == "" {
		return nil, errors.New("oauth token secret is empty")
	}
	if len(value)%2 != 0 {
		return nil, errors.New("oauth token secret has odd hex length")
	}
	secret, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("oauth token secret is not hex: %w", err)
	}
	if len(secret) < tokenSecretBytes {
		return nil, fmt.Errorf("oauth token secret must be at least %d bytes", tokenSecretBytes)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure oauth token secret: %w", err)
	}
	return secret, nil
}
