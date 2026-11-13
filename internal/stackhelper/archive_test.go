package stackhelper

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type entry struct {
	name, link, body string
	typ              byte
	uid              int
	mode             int64
}

func collect(t *testing.T, h *Helper, base, dir string) []entry {
	t.Helper()
	var out []entry
	err := h.Walk(context.Background(), base, dir, func(hdr *tar.Header, r io.Reader) error {
		e := entry{name: hdr.Name, link: hdr.Linkname, typ: hdr.Typeflag, uid: hdr.Uid, mode: hdr.Mode}
		if r != nil {
			b, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			e.body = string(b)
		}
		out = append(out, e)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWalkReadsDirectlyWhenItCan(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "web")
	if err := os.MkdirAll(filepath.Join(dir, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "conf", "passwd")); err != nil {
		t.Fatal(err)
	}
	f := &fakeDocker{haveImage: true}
	got := collect(t, New(f), base, dir)
	if len(f.created) != 0 {
		t.Fatal("helper used for a tree the service can read")
	}
	want := []entry{
		{name: "compose.yaml", body: "services: {}\n", typ: tar.TypeReg, mode: 0o640},
		{name: "conf", typ: tar.TypeDir, mode: 0o755},
		{name: "conf/passwd", link: "/etc/passwd", typ: tar.TypeSymlink},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		g := got[i]
		g.uid = 0
		if w.typ == tar.TypeSymlink {
			g.mode = 0
		}
		if g != w {
			t.Errorf("entry %d = %+v, want %+v", i, g, w)
		}
	}
}

func helperArchive(t *testing.T, hdrs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hdrs {
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = "data of " + h.Name
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWalkThroughHelper(t *testing.T) {
	base, dir, _ := lockedStack(t)
	f := &fakeDocker{haveImage: true, archive: helperArchive(t,
		&tar.Header{Name: "stack/", Typeflag: tar.TypeDir, Mode: 0o755},
		&tar.Header{Name: "stack/compose.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 1000},
		&tar.Header{Name: "stack/pgdata/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 999},
		&tar.Header{Name: "stack/pgdata/PG_VERSION", Typeflag: tar.TypeReg, Mode: 0o600, Uid: 999},
		&tar.Header{Name: "stack/pgdata/.s.PGSQL.5432", Typeflag: tar.TypeFifo, Mode: 0o600, Uid: 999},
	)}
	got := collect(t, New(f), base, dir)
	want := []entry{
		{name: "compose.yaml", body: "data of stack/compose.yaml", typ: tar.TypeReg, uid: 1000, mode: 0o644},
		{name: "pgdata", typ: tar.TypeDir, uid: 999, mode: 0o700},
		{name: "pgdata/PG_VERSION", body: "data of stack/pgdata/PG_VERSION", typ: tar.TypeReg, uid: 999, mode: 0o600},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(f.created) != 1 || f.removed != 1 {
		t.Fatalf("created %d helpers, removed %d", len(f.created), f.removed)
	}
	if m := f.created[0].HostConfig.Mounts[0]; !m.ReadOnly {
		t.Error("backup mounts the stack dir read-write")
	}
	if len(f.created[0].HostConfig.CapAdd) != 0 {
		t.Errorf("reading needs no capabilities, got %v", f.created[0].HostConfig.CapAdd)
	}
}

func TestWalkRefusesEntryOutsideTheMount(t *testing.T) {
	base, dir, _ := lockedStack(t)
	f := &fakeDocker{haveImage: true, archive: helperArchive(t,
		&tar.Header{Name: "stack/../etc/passwd", Typeflag: tar.TypeReg, Mode: 0o644},
	)}
	err := New(f).Walk(context.Background(), base, dir, func(*tar.Header, io.Reader) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "leaves the stack directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtract(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "db")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeDocker{haveImage: true}
	in := helperArchive(t, &tar.Header{Name: "pgdata/", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 999, ModTime: time.Unix(1e9, 0)})
	if err := New(f).Extract(context.Background(), base, dir, bytes.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.extracted, in) {
		t.Error("archive not passed to the daemon unchanged")
	}
	if m := f.created[0].HostConfig.Mounts[0]; m.ReadOnly || !m.BindOptions.NonRecursive {
		t.Errorf("mount = %+v", m)
	}
	if err := New(f).Extract(context.Background(), base, base, bytes.NewReader(in)); err == nil {
		t.Error("extracted into the stacks root itself")
	}
}
