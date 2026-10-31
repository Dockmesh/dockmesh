//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"testing"

	"filippo.io/age"
)

// TestEncryptedStream decrypts archives straight from the target's reader,
// the way restore and verify consume them. age needs a clean io.EOF after
// the last chunk, so a target whose final read fails loses the tail of
// every encrypted archive even though the stored file is intact.
func TestEncryptedStream(t *testing.T) {
	env := LoadEnv(t)
	base := RunID()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range env.Targets(base) {
		t.Run(c.Name, func(t *testing.T) {
			if c.Gaps[GapAll] != "" {
				t.Skip(c.Gaps[GapAll])
			}
			tg := build(t, c, c.Config)
			for _, size := range []int{1, 64 << 10, 87000, 1 << 20} {
				t.Run(fmt.Sprint(size), func(t *testing.T) {
					plain := make([]byte, size)
					if _, err := rand.Read(plain); err != nil {
						t.Fatal(err)
					}
					var stored bytes.Buffer
					w, err := age.Encrypt(&stored, id.Recipient())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := w.Write(plain); err != nil {
						t.Fatal(err)
					}
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
					path := fmt.Sprintf("stream/%d.tar.gz.age", size)
					writeFile(t, tg, path, stored.Bytes())

					rc, err := tg.Read(context.Background(), path)
					if err != nil {
						t.Fatal(err)
					}
					defer rc.Close()
					dec, err := age.Decrypt(rc, id)
					if err != nil {
						t.Fatal(err)
					}
					got, err := io.ReadAll(dec)
					if err != nil {
						t.Fatalf("decrypt from target after %d of %d bytes: %v", len(got), size, err)
					}
					if !bytes.Equal(got, plain) {
						t.Fatalf("decrypted %d bytes that differ from the %d written", len(got), size)
					}
				})
			}
		})
	}
}
