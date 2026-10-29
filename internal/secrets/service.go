// Package secrets wraps filippo.io/age for at-rest encryption of stack
// `.env` files. The plaintext never touches disk
// when secrets are enabled: we decrypt to memory, hand the values to the
// compose loader, and the DB/filesystem only ever hold the ciphertext.
package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	// previous holds every other key from the history file. Decrypt tries
	// them after the current one, so data encrypted before (or during an
	// interrupted) rotation stays readable.
	previous []*age.X25519Identity
}

// historyPath is where keys other than the current one are kept.
func (s *Service) historyPath() string { return s.keyPath + ".history" }

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

// Identities returns the current key followed by every previous one, for
// callers that decrypt with age directly (the backup restore path).
func (s *Service) Identities() ([]age.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identity == nil {
		return nil, errors.New("secrets service has no identity loaded")
	}
	ids := []age.Identity{s.identity}
	for _, p := range s.previous {
		ids = append(ids, p)
	}
	return ids, nil
}

// ExportKeyFile returns the age-keygen-compatible key file contents so
// operators can save the DR key out of band: without it the server's own
// encrypted backup can't be decrypted after a total server loss (the key
// lives inside the encrypted archive).
//
// Format: age-keygen-compatible — dockmesh or `age` CLI can consume it.
func (s *Service) ExportKeyFile() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identity == nil {
		return "", errors.New("secrets service has no identity loaded")
	}
	// The current key first (parseKeyFile takes it as current), then the
	// previous ones so backups from before a rotation can be restored.
	return formatKeys(append([]*age.X25519Identity{s.identity}, s.previous...)...), nil
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
		previous:   append([]*age.X25519Identity(nil), s.previous...),
	}
}

// RotateInMemory generates a fresh key and returns a second Service
// that wraps it without touching disk. Callers use this pair to
// re-encrypt existing ciphertexts (old=existing service, new=returned
// value): call PrepareRotation before re-encrypting and AdoptAndPersist
// afterwards; discard the returned Service to abort.
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

// PrepareRotation records the current and the new key in the history file
// before anything is re-encrypted. Whatever step a rotation is interrupted
// at, both keys stay available to Decrypt after a restart.
func (s *Service) PrepareRotation(newSvc *Service) error {
	if newSvc == nil || newSvc.identity == nil {
		return errors.New("rotate: new service has no identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity == nil {
		return errors.New("rotate: live service has no identity")
	}
	keys := append([]*age.X25519Identity{s.identity, newSvc.identity}, s.previous...)
	if err := writeFileAtomic(s.historyPath(), []byte(formatKeys(keys...)), 0o400); err != nil {
		return fmt.Errorf("write key history: %w", err)
	}
	// Stacks re-encrypted before a failure must stay readable without a
	// restart, so the new key is known for decryption right away.
	s.previous = append(s.previous, newSvc.identity)
	return nil
}

// AdoptAndPersist makes newSvc's key the current one: it atomically
// replaces the key file and swaps the in-memory identity. Call
// PrepareRotation first so the old key is in the history.
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
	if err := writeFileAtomic(s.keyPath, []byte(formatKeys(newSvc.identity)), 0o400); err != nil {
		return oldRecipient, fmt.Errorf("write new key: %w", err)
	}
	previous := []*age.X25519Identity{s.identity}
	for _, p := range s.previous {
		if p.String() != newSvc.identity.String() {
			previous = append(previous, p)
		}
	}
	s.identity = newSvc.identity
	s.recipients = []age.Recipient{newSvc.identity.Recipient()}
	s.previous = previous
	return oldRecipient, nil
}

func formatKeys(ids ...*age.X25519Identity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# created: %s\n", time.Now().UTC().Format(time.RFC3339))
	for _, id := range ids {
		fmt.Fprintf(&b, "# public key: %s\n%s\n", id.Recipient(), id)
	}
	return b.String()
}

// writeFileAtomic writes data to a temp file next to path, syncs it and
// renames it over path, so a crash leaves either the old or the new
// content, never a missing or partial file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	// Windows refuses to rename over a read-only file; the old file is
	// replaced in the next step anyway.
	_ = os.Chmod(path, 0o600)
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func (s *Service) loadOrCreateKey() error {
	b, err := os.ReadFile(s.keyPath)
	switch {
	case err == nil:
		if err := s.parseKeyFile(b); err != nil {
			return err
		}
	case errors.Is(err, os.ErrNotExist):
		if err := s.generateKey(); err != nil {
			return err
		}
	default:
		return err
	}
	return s.loadHistory()
}

// loadHistory reads previous keys from the history file and from the
// ".old" file earlier versions left after a rotation.
func (s *Service) loadHistory() error {
	seen := map[string]bool{s.identity.String(): true}
	for _, p := range s.previous {
		seen[p.String()] = true
	}
	for _, path := range []string{s.historyPath(), s.keyPath + ".old"} {
		legacy := path != s.historyPath()
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var ids []*age.X25519Identity
		if err == nil {
			ids, err = parseIdentities(b)
		}
		if err != nil {
			// The .old file is a leftover of earlier versions; it must not
			// keep the server from starting.
			if legacy {
				slog.Warn("ignoring unreadable previous key file", "file", filepath.Base(path), "err", err)
				continue
			}
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		for _, id := range ids {
			if !seen[id.String()] {
				seen[id.String()] = true
				s.previous = append(s.previous, id)
			}
		}
	}
	return nil
}

// parseKeyFile reads an age-keygen-style file. Supported layout:
//
//	# created: <ts>
//	# public key: age1...
//	AGE-SECRET-KEY-1...
//
// Lines beginning with "#" are skipped.
func (s *Service) parseKeyFile(b []byte) error {
	ids, err := parseIdentities(b)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("no AGE-SECRET-KEY line found in key file")
	}
	s.identity = ids[0]
	s.recipients = []age.Recipient{ids[0].Recipient()}
	// An exported key file lists earlier keys after the current one.
	s.previous = ids[1:]
	return nil
}

func parseIdentities(b []byte) ([]*age.X25519Identity, error) {
	var ids []*age.X25519Identity
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "AGE-SECRET-KEY-") {
			continue
		}
		id, err := age.ParseX25519Identity(line)
		if err != nil {
			return nil, fmt.Errorf("parse identity: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Service) generateKey() error {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.keyPath, []byte(formatKeys(id)), 0o400); err != nil {
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

// Decrypt reverses Encrypt, trying the current key and then every key in
// the history. Returns the input unchanged when the service is disabled.
func (s *Service) Decrypt(ciphertext []byte) ([]byte, error) {
	if !s.Enabled() {
		return ciphertext, nil
	}
	ids, err := s.Identities()
	if err != nil {
		return ciphertext, nil
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), ids...)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ParseKeyFile reads the identities of an exported backup key file (or
// a secrets.age-key file), for decrypting backups of another install.
func ParseKeyFile(b []byte) ([]age.Identity, error) {
	ids, err := parseIdentities(b)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errors.New("no AGE-SECRET-KEY line in the key file")
	}
	out := make([]age.Identity, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out, nil
}
