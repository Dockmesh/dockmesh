package targets

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func hostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSFTPHostKeyTrustOnFirstUse(t *testing.T) {
	first, other := hostKey(t), hostKey(t)
	s, err := NewSFTP(map[string]any{"host": "backup.example"})
	if err != nil {
		t.Fatal(err)
	}
	var events []HostKeyEvent
	s.OnHostKey(func(ev HostKeyEvent) { events = append(events, ev) })

	if err := s.checkHostKey("", nil, first); err != nil {
		t.Fatalf("first contact refused: %v", err)
	}
	if err := s.checkHostKey("", nil, first); err != nil {
		t.Fatalf("same key refused: %v", err)
	}
	err = s.checkHostKey("", nil, other)
	var changed *HostKeyChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("changed key: err = %v, want HostKeyChangedError", err)
	}
	if changed.Old != ssh.FingerprintSHA256(first) || changed.New != ssh.FingerprintSHA256(other) {
		t.Errorf("fingerprints = %+v", changed)
	}
	kinds := []string{}
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	if strings.Join(kinds, ",") != "recorded,matched,changed" {
		t.Errorf("events = %v", kinds)
	}
}

func TestLocalTargetRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "backups")
	outside := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	SetLocalRoots(func() []string { return []string{root} })
	t.Cleanup(func() { SetLocalRoots(nil) })

	if _, err := NewLocal(map[string]any{"path": filepath.Join(root, "nightly")}); err != nil {
		t.Errorf("path under the root refused: %v", err)
	}
	for _, p := range []string{outside, filepath.Join(root, "..", "elsewhere"), "/etc"} {
		if _, err := NewLocal(map[string]any{"path": p}); !errors.Is(err, ErrLocalPathNotAllowed) {
			t.Errorf("%s: err = %v, want ErrLocalPathNotAllowed", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); !os.IsNotExist(err) {
		t.Error("a refused target created a directory")
	}

	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if _, err := NewLocal(map[string]any{"path": filepath.Join(root, "link", "x")}); !errors.Is(err, ErrLocalPathNotAllowed) {
		t.Errorf("symlink out of the root: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); !os.IsNotExist(err) {
		t.Error("target created a directory through a symlink out of the root")
	}
}

func TestParseRoots(t *testing.T) {
	got := ParseRoots(" /srv/backups \n\n/var/lib/dockmesh/data/backups,/mnt/nas ")
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}
