package releasesig

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"aead.dev/minisign"
)

type testKey struct {
	pub  string
	priv minisign.PrivateKey
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{pub: pub.String(), priv: priv}
}

func checksumsFor(files map[string][]byte) []byte {
	var b strings.Builder
	for name, content := range files {
		sum := sha256.Sum256(content)
		b.WriteString(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	}
	return []byte(b.String())
}

func TestEmbeddedKeys(t *testing.T) {
	v := Default()
	want := []uint64{0x01A0941E358E1C5A, 0x73BBC751F8715F03}
	if len(v.keys) != len(want) {
		t.Fatalf("got %d keys, want %d", len(v.keys), len(want))
	}
	for i, id := range want {
		if v.keys[i].ID() != id {
			t.Errorf("key %d: id %016X, want %016X", i, v.keys[i].ID(), id)
		}
	}
}

func TestReleaseKeyID(t *testing.T) {
	if got := ReleaseKeyID(); got != "01A0941E358E1C5A" {
		t.Fatalf("ReleaseKeyID() = %s", got)
	}
}

func TestVerifyChecksums(t *testing.T) {
	release, rotation, stranger := newTestKey(t), newTestKey(t), newTestKey(t)
	v, err := New(release.pub, rotation.pub)
	if err != nil {
		t.Fatal(err)
	}
	checksums := checksumsFor(map[string][]byte{"dockmesh_linux_amd64.tar.gz": []byte("binary")})
	sign := func(k testKey, msg []byte, comment string) []byte {
		return minisign.SignWithComments(k.priv, msg, comment, "test signature")
	}
	good := sign(release, checksums, TrustedComment("v0.6.0"))
	r := minisign.NewReader(bytes.NewReader(checksums))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	prehashed := r.SignWithComments(release.priv, TrustedComment("v0.6.0"), "")

	tests := []struct {
		name      string
		checksums []byte
		sig       []byte
		tag       string
		wantKey   uint64
		wantErr   error
	}{
		{"release key", checksums, good, "v0.6.0", release.priv.ID(), nil},
		{"rotation key", checksums, sign(rotation, checksums, TrustedComment("v0.6.0")), "v0.6.0", rotation.priv.ID(), nil},
		{"pre-release tag", checksums, sign(release, checksums, TrustedComment("v0.6.0-rc.1")), "v0.6.0-rc.1", release.priv.ID(), nil},
		{"prehashed signature", checksums, prehashed, "v0.6.0", release.priv.ID(), nil},
		{"signature of another tag", checksums, good, "v0.6.1", 0, ErrTag},
		{"trusted comment of a prefix tag", checksums, sign(release, checksums, TrustedComment("v0.6.0")), "v0.6.0-rc.1", 0, ErrTag},
		{"trusted comment without the tag", checksums, sign(release, checksums, "timestamp:1"), "v0.6.0", 0, ErrTag},
		{"tampered checksums", append(bytes.Clone(checksums), '\n'), good, "v0.6.0", 0, ErrSignature},
		{"edited trusted comment", checksums, bytes.Replace(good, []byte("v0.6.0"), []byte("v0.6.1"), 1), "v0.6.1", 0, ErrSignature},
		{"unknown key", checksums, sign(stranger, checksums, TrustedComment("v0.6.0")), "v0.6.0", 0, ErrSignature},
		{"garbage", checksums, []byte("not a signature"), "v0.6.0", 0, ErrSignature},
		{"empty", checksums, nil, "v0.6.0", 0, ErrSignature},
		{"tag with a space", checksums, sign(release, checksums, TrustedComment("v0.6.0 x")), "v0.6.0 x", 0, ErrTag},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := v.VerifyChecksums(tt.checksums, tt.sig, tt.tag)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if id != tt.wantKey {
				t.Fatalf("key id = %016X, want %016X", id, tt.wantKey)
			}
		})
	}
}

func TestVerifyArtifact(t *testing.T) {
	key := newTestKey(t)
	v, err := New(key.pub)
	if err != nil {
		t.Fatal(err)
	}
	const name = "dockmesh_linux_amd64.tar.gz"
	artifact := []byte("binary")
	checksums := checksumsFor(map[string][]byte{name: artifact, "install.sh": []byte("#!/bin/sh")})
	sig := minisign.SignWithComments(key.priv, checksums, TrustedComment("v0.6.0"), "")
	dup := append(bytes.Clone(checksums), checksumsFor(map[string][]byte{name: []byte("other")})...)

	tests := []struct {
		name      string
		checksums []byte
		sig       []byte
		file      string
		content   []byte
		wantErr   error
	}{
		{"matches", checksums, sig, name, artifact, nil},
		{"tampered artifact", checksums, sig, name, []byte("binarY"), ErrChecksum},
		{"not listed", checksums, sig, "dockmesh_linux_arm64.tar.gz", artifact, ErrNotListed},
		{"bad signature", checksums, sig[:len(sig)-10], name, artifact, ErrSignature},
		{"listed twice", dup, minisign.SignWithComments(key.priv, dup, TrustedComment("v0.6.0"), ""), name, artifact, ErrDuplicate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.VerifyArtifact(tt.checksums, tt.sig, "v0.6.0", tt.file, bytes.NewReader(tt.content))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestChecksum(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	tests := []struct {
		name, file, want string
		ok               bool
	}{
		{"text mode", sum + "  a.tar.gz\n", sum, true},
		{"binary mode", sum + " *a.tar.gz\n", sum, true},
		{"suffix match only", sum + "  xa.tar.gz\n", "", false},
		{"short sum", "abcd  a.tar.gz\n", "", false},
		{"not hex", strings.Repeat("zz", 32) + "  a.tar.gz\n", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Checksum([]byte(tt.file), "a.tar.gz")
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && hex.EncodeToString(got) != tt.want {
				t.Fatalf("got %x, want %s", got, tt.want)
			}
		})
	}
}
