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
	"sync"
	"time"

	"filippo.io/age"
)

type Service struct {
	enabled    bool
	keyPath    string
	mu         sync.RWMutex
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identity == nil {
		return ""
	}
	return s.identity.Recipient().String()
}

// Identity returns the loaded X25519 identity. Used by callers that need
// to decrypt with the same key (e.g. the backup restore path).
func (s *Service) Identity() (age.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identity == nil {
		return nil, errors.New("secrets service has no identity loaded")
	}
	return s.identity, nil
}

// ExportKeyFile returns the age-keygen-compatible key file contents so
// operators can save the DR key out of band. Fixes FINDING-37: without
// a way to retrieve this file the server's own encrypted backup can't
// be decrypted after a total server loss (key lives inside the
// encrypted archive — chicken-and-egg).
//
// Format: age-keygen-compatible — dockmesh or `age` CLI can consume it.
func (s *Service) ExportKeyFile() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identity == nil {
		return "", errors.New("secrets service has no identity loaded")
	}
	pub := s.identity.Recipient().String()
	sec := s.identity.String()
	return "# created: " + time.Now().UTC().Format(time.RFC3339) + "\n" +
		"# public key: " + pub + "\n" +
		sec + "\n", nil
}

// Snapshot returns a detached, read-only copy of the current Service
// state. The snapshot keeps the *current* identity in memory so callers
// can migrate legacy ciphertexts even after the live Service has
// rotated to a new key. Disk IO is disabled on the snapshot.
func (s *Service) Snapshot() *Service {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &Service{
		enabled:    s.enabled,
		keyPath:    "",
		identity:   s.identity,
		recipients: append([]age.Recipient(nil), s.recipients...),
	}
}

// RotateInMemory generates a fresh key and returns a second Service
// that wraps it without touching disk. Callers use this pair to
// re-encrypt legacy ciphertexts (old=existing service, new=returned
// value). Commit the rotation with AdoptAndPersist once the migration
// succeeds; discard the returned Service to abort.
func (s *Service) RotateInMemory() (*Service, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	return &Service{
		enabled:    true,
		keyPath:    "",
		identity:   id,
		recipients: []age.Recipient{id.Recipient()},
	}, nil
}

// AdoptAndPersist atomically swaps the live service's in-memory
// identity to match newSvc and persists newSvc's private key to
// keyPath. The previous key file is moved to keyPath+".old" so legacy
// external backups can still be decrypted out-of-band.
//
// Returns the old recipient so callers can surface it in audit logs.
func (s *Service) AdoptAndPersist(newSvc *Service) (oldRecipient string, err error) {
	if newSvc == nil || newSvc.identity == nil {
		return "", errors.New("adopt: new service has no identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity == nil {
		return "", errors.New("adopt: live service has no identity")
	}
	oldRecipient = s.identity.Recipient().String()
	// Move current key aside. Stat/rename rather than clobber the
	// .old slot — the previous .old may itself be needed to decrypt
	// a backup sitting in S3 somewhere; we only keep N-1.
	oldPath := s.keyPath + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(s.keyPath, oldPath); err != nil {
		return oldRecipient, fmt.Errorf("archive old key: %w", err)
	}
	content := fmt.Sprintf(
		"# created: %s\n# public key: %s\n%s\n",
		time.Now().UTC().Format(time.RFC3339),
		newSvc.identity.Recipient(), newSvc.identity,
	)
	if err := os.WriteFile(s.keyPath, []byte(content), 0o400); err != nil {
		// Roll back so the service stays usable.
		_ = os.Rename(oldPath, s.keyPath)
		return oldRecipient, fmt.Errorf("write new key: %w", err)
	}
	s.identity = newSvc.identity
	s.recipients = []age.Recipient{newSvc.identity.Recipient()}
	return oldRecipient, nil
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
	s.mu.RLock()
	recipients := s.recipients
	enabled := s.enabled
	s.mu.RUnlock()
	if !enabled || len(recipients) == 0 {
		return plaintext, nil
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipients...)
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
	s.mu.RLock()
	id := s.identity
	enabled := s.enabled
	s.mu.RUnlock()
	if !enabled || id == nil {
		return ciphertext, nil
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), id)
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
