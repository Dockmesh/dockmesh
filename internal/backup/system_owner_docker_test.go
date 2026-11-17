//go:build integration

package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/restore"
	"github.com/dockmesh/dockmesh/internal/stackhelper"
	"github.com/moby/moby/client"
)

// A postgres data directory (uid 999, 0700) that the unprivileged service
// can neither read nor write goes into the system backup and comes back
// with its owner and mode.
func TestDockerSystemRoundTripKeepsContainerOwners(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an unprivileged user")
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Skip(err)
	}
	defer cli.Close()
	h := stackhelper.New(cli)

	src := systemSource(t)
	pg := filepath.Join(src.StacksRoot, "pg")
	mkdirMode(t, pg, 0o750)
	t.Cleanup(func() { _ = h.RemoveAll(ctx, src.StacksRoot, pg) })
	var seed bytes.Buffer
	tw := tar.NewWriter(&seed)
	for _, hdr := range []*tar.Header{
		{Name: "data", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 999, Gid: 999},
		{Name: "data/PG_VERSION", Typeflag: tar.TypeReg, Mode: 0o600, Uid: 999, Gid: 999, Size: 3},
	} {
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte("17\n")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Extract(ctx, src.StacksRoot, pg, &seed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadDir(filepath.Join(pg, "data")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("seeded data dir is readable by the test user (err %v)", err)
	}

	database, err := dbtest.Open(src.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var buf bytes.Buffer
	if _, err := tarSystem(ctx, database, src, h, &buf); err != nil {
		t.Fatal(err)
	}
	if hdr := archiveHeader(t, buf.Bytes(), "stacks/pg/data/PG_VERSION"); hdr == nil || hdr.Uid != 999 {
		t.Fatalf("archive entry for PG_VERSION = %+v, want uid 999", hdr)
	}

	cfg := restoreTarget(t)
	t.Cleanup(func() { _ = h.RemoveAll(ctx, cfg.StacksRoot, filepath.Join(cfg.StacksRoot, "pg")) })
	counts := stageAndApply(t, cfg, buf.Bytes(), restore.Helper(h))
	if counts.LostOwners != 0 {
		t.Errorf("LostOwners = %d", counts.LostOwners)
	}
	fi, err := os.Lstat(filepath.Join(cfg.StacksRoot, "pg", "data"))
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 999 || st.Gid != 999 || fi.Mode().Perm() != 0o700 {
		t.Errorf("restored pg/data: %d:%d %#o, want 999:999 0700", st.Uid, st.Gid, fi.Mode().Perm())
	}
	wantModes(t, cfg, restoredModes)

	// A second restore moves this one aside; deleting the copy needs the
	// helper as well. Copies are named by the second.
	time.Sleep(1100 * time.Millisecond)
	stageAndApply(t, cfg, buf.Bytes(), restore.Helper(h))
	if _, err := restore.RemoveLeftovers(ctx, cfg, restore.Helper(h)); err != nil {
		t.Fatal(err)
	}
	if left, err := restore.Leftovers(cfg); err != nil || len(left) != 0 {
		t.Errorf("leftovers after removal: %v (err %v)", left, err)
	}
}

func archiveHeader(t *testing.T, archive []byte, name string) *tar.Header {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == name {
			return hdr
		}
	}
}
