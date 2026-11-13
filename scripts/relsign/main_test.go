package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "checksums.txt")
	sig := filepath.Join(dir, "checksums.txt.minisig")
	if err := os.WriteFile(in, []byte("00  a.tar.gz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sig, sign(priv, []byte("00  a.tar.gz\n"), "v0.6.0-rc.1"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%016X", priv.ID())

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"own key", []string{"-tag", "v0.6.0-rc.1", "-pubkey", pub.String()}, ""},
		{"required key id", []string{"-tag", "v0.6.0-rc.1", "-pubkey", other.String(), "-pubkey", pub.String(), "-key-id", id}, ""},
		{"other key id required", []string{"-tag", "v0.6.0-rc.1", "-pubkey", pub.String(), "-key-id", fmt.Sprintf("%016X", other.ID())}, "want"},
		{"other tag", []string{"-tag", "v0.6.0", "-pubkey", pub.String()}, "different release"},
		{"embedded keys", []string{"-tag", "v0.6.0-rc.1"}, "unknown key"},
		{"release key required", []string{"-tag", "v0.6.0-rc.1", "-pubkey", pub.String(), "-release-key"}, "want 01A0941E358E1C5A"},
		{"both key flags", []string{"-tag", "v0.6.0-rc.1", "-release-key", "-key-id", id}, "exclude"},
		{"not a tag", []string{"-tag", "latest", "-pubkey", pub.String()}, "not a release tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"verify", "-in", in, "-sig", sig}, tt.args...)
			err := run(args, io.Discard, func(string) string { return "" })
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("verify: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestSignRefusesWithoutSecret(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(in, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"MINISIGN_PASSWORD": "pw"}
	err := run([]string{"sign", "-tag", "v0.6.0", "-in", in, "-out", filepath.Join(dir, "sig")}, io.Discard, func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "MINISIGN_SECRET_KEY") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sig")); !os.IsNotExist(err) {
		t.Fatalf("signature written without a key: %v", err)
	}
}
