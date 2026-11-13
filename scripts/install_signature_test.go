package scripts

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"

	"github.com/dockmesh/dockmesh/internal/releasesig"
)

func TestInstallReleaseKeysMatchBinary(t *testing.T) {
	out, err := runVersionHelper(t, `printf '%s' "$RELEASE_PUBKEYS"`)
	if err != nil {
		t.Fatal(err)
	}
	if want := releasesig.ReleaseKey + " " + releasesig.RotationKey; out != want {
		t.Fatalf("RELEASE_PUBKEYS = %q, want %q", out, want)
	}
}

func TestInstallVerifyReleaseSignature(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	if _, err := runVersionHelper(t, "signature_tool_ok"); err != nil {
		t.Skip("openssl on this host is older than 3.0")
	}
	gen := func() (minisign.PublicKey, minisign.PrivateKey) {
		pub, priv, err := minisign.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return pub, priv
	}
	releasePub, release := gen()
	rotationPub, rotation := gen()
	_, stranger := gen()
	keys := releasePub.String() + " " + rotationPub.String()

	checksums := []byte(strings.Repeat("ab", 32) + "  dockmesh_linux_amd64.tar.gz\n")
	sign := func(k minisign.PrivateKey, tag string) []byte {
		return minisign.SignWithComments(k, checksums, releasesig.TrustedComment(tag), "test")
	}
	good := sign(release, "v0.6.0")
	r := minisign.NewReader(bytes.NewReader(checksums))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	prehashed := r.SignWithComments(release, releasesig.TrustedComment("v0.6.0"), "test")

	tests := []struct {
		name      string
		checksums []byte
		sig       []byte
		tag       string
		wantKey   string
	}{
		{"release key", checksums, good, "v0.6.0", releaseKeyHex(release)},
		{"rotation key", checksums, sign(rotation, "v0.6.0"), "v0.6.0", releaseKeyHex(rotation)},
		{"release candidate", checksums, sign(release, "v0.6.0-rc.2"), "v0.6.0-rc.2", releaseKeyHex(release)},
		{"signature for another tag", checksums, good, "v0.6.1", ""},
		{"tampered checksums", append(bytes.Clone(checksums), 'x'), good, "v0.6.0", ""},
		{"edited trusted comment", checksums, bytes.Replace(good, []byte("v0.6.0"), []byte("v0.6.1"), 1), "v0.6.1", ""},
		{"unknown key", checksums, sign(stranger, "v0.6.0"), "v0.6.0", ""},
		{"prehashed signature", checksums, prehashed, "v0.6.0", ""},
		{"extra lines", checksums, append(bytes.Clone(good), []byte("trusted comment: x\n")...), "v0.6.0", ""},
		{"empty signature", checksums, nil, "v0.6.0", ""},
		{"garbage", checksums, []byte("untrusted comment: x\n!!!\ntrusted comment: dockmesh v0.6.0 checksums.txt\n!!!\n"), "v0.6.0", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			sumFile := filepath.Join(dir, "checksums.txt")
			sigFile := filepath.Join(dir, "checksums.txt.minisig")
			work := filepath.Join(dir, "work")
			for path, content := range map[string][]byte{sumFile: tt.checksums, sigFile: tt.sig} {
				if err := os.WriteFile(path, content, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			out, err := runVersionHelper(t, `verify_release_signature "$@"`, sumFile, sigFile, tt.tag, keys, work)
			if tt.wantKey == "" {
				if err == nil {
					t.Fatalf("verified, want refusal (output %q)", out)
				}
				return
			}
			if err != nil || out != tt.wantKey {
				t.Fatalf("got %q (err %v), want key %s", out, err, tt.wantKey)
			}
		})
	}
}

func releaseKeyHex(k minisign.PrivateKey) string {
	return fmt.Sprintf("%016X", k.ID())
}

func TestInstallSignatureToolOK(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"OpenSSL 3.0.13 30 Jan 2024 (Library: OpenSSL 3.0.13 30 Jan 2024)", true},
		{"OpenSSL 3.5.0 8 Apr 2025", true},
		{"OpenSSL 1.1.1w  11 Sep 2023", false},
		{"LibreSSL 3.3.6", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			bin := t.TempDir()
			script := "#!/bin/sh\necho '" + tt.version + "'\n"
			if err := os.WriteFile(filepath.Join(bin, "openssl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			_, err := runVersionHelper(t, `PATH="$1:$PATH"; signature_tool_ok`, bin)
			if got := err == nil; got != tt.want {
				t.Fatalf("signature_tool_ok = %v, want %v", got, tt.want)
			}
		})
	}
}
