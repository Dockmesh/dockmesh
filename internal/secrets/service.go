// Package secrets wraps filippo.io/age for at-rest encryption of stack
// `.env` files (concept §15.2 Phase 2). The plaintext never touches disk
// when secrets are enabled: we decrypt to memory, hand the values to the
// compose loader, and the DB/filesystem only ever hold the ciphertext.
package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

type Service struct {
	enabled    bool
	keyPath    string
	identity   *age.X25519Identity
	recipients []age.Recipient
}

// New returns a Service that loads or creates the age key at keyPath.
// If enabled is false the Service is a no-op passthrough.
func New(keyPath string, enabled bool) (*Service, error) {
	s := &Service{enabled: enabled, keyPath: keyPath}
	if !enabled {
		return s, nil
	}
	if err := s.loadOrCreateKey(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) Enabled() bool { return s.enabled }

// PublicRecipient returns the age1… public recipient string.
func (s *Service) PublicRecipient() string {
	if s.identity == nil {
		return ""
	}
	return s.identity.Recipient().String()
}

func (s *Service) loadOrCreateKey() error {
	b, err := os.ReadFile(s.keyPath)
	switch {
	case err == nil:
		return s.parseKeyFile(b)
	case errors.Is(err, os.ErrNotExist):
		return s.generateKey()
	default:
		return err
	}
}

// parseKeyFile reads an age-keygen-style file. Supported layout:
//   # created: <ts>
//   # public key: age1...
//   AGE-SECRET-KEY-1...
// Lines beginning with "#" are skipped.
func (s *Service) parseKeyFile(b []byte) error {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "AGE-SECRET-KEY-") {
			continue
		}
		id, err := age.ParseX25519Identity(line)
		if err != nil {
			return fmt.Errorf("parse identity: %w", err)
		}
		s.identity = id
		s.recipients = []age.Recipient{id.Recipient()}
		return nil
	}
	return errors.New("no AGE-SECRET-KEY line found in key file")
}

func (s *Service) generateKey() error {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.keyPath), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf("# public key: %s\n%s\n", id.Recipient(), id)
	if err := os.WriteFile(s.keyPath, []byte(content), 0o400); err != nil {
		return err
	}
	s.identity = id
	s.recipients = []age.Recipient{id.Recipient()}
	return nil
}

// Encrypt wraps plaintext as an age ciphertext. Returns the original bytes
// if the service is disabled.
func (s *Service) Encrypt(plaintext []byte) ([]byte, error) {
	if !s.enabled || len(s.recipients) == 0 {
		return plaintext, nil
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, s.recipients...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decrypt reverses Encrypt. Returns the input unchanged when the service
// is disabled or when the bytes are not a valid age stream but look like
// plaintext (backward compatibility with legacy `.env` files).
func (s *Service) Decrypt(ciphertext []byte) ([]byte, error) {
	if !s.enabled || s.identity == nil {
		return ciphertext, nil
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), s.identity)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ArchiveKey moves the current key file to keyPath + ".old" and forgets
// the identity. Used by the rotation command before generating a fresh key.
func (s *Service) ArchiveKey() error {
	if _, err := os.Stat(s.keyPath); err != nil {
		return err
	}
	old := s.keyPath + ".old"
	_ = os.Remove(old)
	return os.Rename(s.keyPath, old)
}
