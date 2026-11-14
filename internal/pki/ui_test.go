package pki

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestEnsureUICertIsKept(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.EnsureUICert([]string{"dm.lan", "192.0.2.10", "bad name!"})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Leaf.VerifyHostname("dm.lan"); err != nil {
		t.Errorf("cert does not cover dm.lan: %v", err)
	}
	if err := first.Leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("cert does not cover loopback: %v", err)
	}
	if err := first.Leaf.CheckSignatureFrom(m.caCert); err != nil {
		t.Errorf("cert not signed by the CA: %v", err)
	}

	// A new start, even with other hosts, serves the same certificate.
	m2, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := m2.EnsureUICert([]string{"other.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if Fingerprint(first.Leaf.Raw) != Fingerprint(again.Leaf.Raw) {
		t.Error("fingerprint changed across starts")
	}
	read, err := ReadUICert(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := Fingerprint(read.Raw)
	if got != Fingerprint(first.Leaf.Raw) {
		t.Errorf("UIFingerprint = %s, want %s", got, Fingerprint(first.Leaf.Raw))
	}
	if !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(got) {
		t.Errorf("fingerprint format %q", got)
	}
}

func TestEnsureUICertReplacesForeignPair(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, UICertFile), other.ServerCertPEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, uiKeyFile), other.ServerKeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := m.EnsureUICert(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Leaf.CheckSignatureFrom(m.caCert); err != nil {
		t.Errorf("a certificate of another CA was kept: %v", err)
	}
}

func TestUIFingerprintRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureUICert(nil); err != nil {
		t.Fatal(err)
	}
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, UICertFile), filepath.Join(linked, UICertFile)); err != nil {
		t.Skip("no symlinks:", err)
	}
	if _, err := ReadUICert(linked); err == nil {
		t.Error("a symlinked certificate was read")
	}
}
