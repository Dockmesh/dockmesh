//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"io"
	"maps"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

func build(t *testing.T, c Config, cfg map[string]any) targets.Target {
	t.Helper()
	tg, err := targets.Build(c.Type, cfg)
	if err != nil {
		t.Fatalf("build %s: %v", c.Name, err)
	}
	return tg
}

func writeFile(t *testing.T, tg targets.Target, path string, data []byte) {
	t.Helper()
	w, err := tg.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func readFile(t *testing.T, tg targets.Target, path string) []byte {
	t.Helper()
	r, err := tg.Read(context.Background(), path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func TestTargets(t *testing.T) {
	env := LoadEnv(t)
	base := RunID()
	for _, c := range env.Targets(base) {
		t.Run(c.Name, func(t *testing.T) {
			if c.Gaps[GapAll] != "" {
				t.Skip(c.Gaps[GapAll])
			}
			ctx := context.Background()
			tg := build(t, c, c.Config)

			t.Run("connection on a new folder", func(t *testing.T) {
				c.SkipGap(t, GapStorageInfoNew)
				if sq, ok := tg.(interface{ StorageInfo() (int64, int64, error) }); ok {
					if _, _, err := sq.StorageInfo(); err != nil {
						t.Fatalf("storage info: %v", err)
					}
				}
			})

			t.Run("list on a new folder", func(t *testing.T) {
				if _, err := tg.List(ctx, ""); err != nil {
					t.Fatalf("list: %v", err)
				}
			})

			t.Run("nested folder roundtrip", func(t *testing.T) {
				data := make([]byte, 3<<20+17)
				_, _ = rand.Read(data)
				want := sha256.Sum256(data)
				path := "jobs/nightly/2026/10/archive.tar.gz"
				writeFile(t, tg, path, data)

				got := readFile(t, tg, path)
				if sha256.Sum256(got) != want {
					t.Fatalf("read back %d bytes, hash differs from the %d written", len(got), len(data))
				}
				if c.Gaps[GapList] == "" {
					entries, err := tg.List(ctx, "jobs/nightly/")
					if err != nil {
						t.Fatal(err)
					}
					if len(entries) != 1 || entries[0].Path != path || entries[0].Size != int64(len(data)) {
						t.Fatalf("list = %+v, want one entry %s of %d bytes", entries, path, len(data))
					}
				}
				if err := tg.Delete(ctx, path); err != nil {
					t.Fatalf("delete: %v", err)
				}
				if r, err := tg.Read(ctx, path); err == nil {
					_, err = io.Copy(io.Discard, r)
					r.Close()
					if err == nil {
						t.Fatal("deleted archive can still be read")
					}
				}
			})

			t.Run("connection", func(t *testing.T) {
				if sq, ok := tg.(interface{ StorageInfo() (int64, int64, error) }); ok {
					if _, _, err := sq.StorageInfo(); err != nil {
						t.Fatalf("storage info: %v", err)
					}
				}
			})

			t.Run("job folders stay apart", func(t *testing.T) {
				c.SkipGap(t, GapList)
				writeFile(t, tg, "jobs/a/one.bin", []byte("one"))
				writeFile(t, tg, "jobs/ab/two.bin", []byte("two"))
				entries, err := tg.List(ctx, "jobs/a/")
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].Path != "jobs/a/one.bin" {
					t.Fatalf("list jobs/a/ = %+v", entries)
				}
			})

			t.Run("wrong credentials fail", func(t *testing.T) {
				bad := maps.Clone(c.Config)
				bad[c.Secret] = wrongSecret(t, c.Secret)
				btg, err := targets.Build(c.Type, bad)
				if err != nil {
					return
				}
				w, err := btg.Open(ctx, "jobs/denied/x.bin")
				if err == nil {
					_, _ = w.Write([]byte("x"))
					err = w.Close()
				}
				if err == nil {
					t.Fatal("write with a wrong secret succeeded")
				}
				if strings.TrimSpace(err.Error()) == "" {
					t.Fatal("error has no message")
				}
			})
		})
	}
}

func wrongSecret(t *testing.T, field string) string {
	if field != "private_key" {
		return "definitely-wrong"
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSpace(pem.EncodeToMemory(blk)))
}
