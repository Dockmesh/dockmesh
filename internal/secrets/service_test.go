package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	plain := []byte("FOO=bar\nBAZ=qux\n")
	ct, err := s.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if string(ct) == string(plain) {
		t.Fatal("ciphertext equals plaintext")
	}
	pt, err := s.Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(pt) != string(plain) {
		t.Errorf("roundtrip mismatch: got %q", pt)
	}

	// Key file should be read-only and carry no write bits. Windows
	// collapses group/other to "read", so we check 0o400 (Linux) OR
	// 0o444 (Windows) — both signal "no writing".
	info, _ := os.Stat(filepath.Join(dir, "key"))
	mode := info.Mode().Perm()
	if mode&0o222 != 0 {
		t.Errorf("key perms = %o, expected read-only", mode)
	}
}

func TestDisabledPassthrough(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "key"), false)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	plain := []byte("hello")
	ct, _ := s.Encrypt(plain)
	if string(ct) != "hello" {
		t.Error("disabled encrypt should passthrough")
	}
	pt, _ := s.Decrypt(ct)
	if string(pt) != "hello" {
		t.Error("disabled decrypt should passthrough")
	}
}

func TestLoadExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")

	// First service generates key
	s1, err := New(path, true)
	if err != nil {
		t.Fatal(err)
	}
	rec1 := s1.PublicRecipient()

	// Second service loads the same key
	s2, err := New(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if s2.PublicRecipient() != rec1 {
		t.Errorf("recipient changed on reload: %q != %q", s2.PublicRecipient(), rec1)
	}
}
