package pki

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestServerCertReissueReplacesReadOnlyKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only files can't be renamed over on Windows; the server runs on Linux")
	}
	dir := t.TempDir()
	if _, err := New(dir, []string{"10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "agents-server.key")
	if fi, err := os.Stat(keyPath); err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("server key mode: %v %v", fi.Mode(), err)
	}

	// A new SAN forces a re-issue over the existing 0400 key.
	m, err := New(dir, []string{"10.0.0.1", "agents.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(filepath.Join(dir, "agents-server.crt"))
	keyPEM, _ := os.ReadFile(keyPath)
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("re-issued cert and key on disk don't match: %v", err)
	}
	if _, err := tls.X509KeyPair(m.ServerCertPEM(), m.ServerKeyPEM()); err != nil {
		t.Fatalf("in-memory cert and key don't match: %v", err)
	}
	if fi, _ := os.Stat(keyPath); fi.Mode().Perm() != 0o400 {
		t.Fatalf("re-issued key mode %v, want 0400", fi.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestServerCertMismatchedKeyIsReissued(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir, []string{"10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	// A key that belongs to a different cert.
	other := t.TempDir()
	if _, err := New(other, []string{"10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	foreignKey, _ := os.ReadFile(filepath.Join(other, "agents-server.key"))
	keyPath := filepath.Join(dir, "agents-server.key")
	_ = os.Chmod(keyPath, 0o600)
	if err := os.WriteFile(keyPath, foreignKey, 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := New(dir, []string{"10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(m.ServerCertPEM(), m.ServerKeyPEM()); err != nil {
		t.Fatalf("mismatched pair was kept: %v", err)
	}
}
