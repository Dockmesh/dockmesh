package restore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type ownedEntry struct {
	name     string
	uid, gid int
	mode     int64
	dir      bool
}

func ownedArchive(t *testing.T, entries ...ownedEntry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Uid: e.uid, Gid: e.gid, Mode: e.mode, Typeflag: tar.TypeReg, Size: 1}
		if e.dir {
			hdr.Typeflag, hdr.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// fakeFiles records what would go through the helper.
type fakeFiles struct {
	dirs    []string
	written map[string]tar.Header
	removed []string
}

func (f *fakeFiles) EntryWriter(_ context.Context, base, dir string) EntryWriter {
	f.dirs = append(f.dirs, dir)
	return &fakeWriter{f: f, base: base, dir: dir}
}

func (f *fakeFiles) RemoveAll(_ context.Context, _, dir string) error {
	f.removed = append(f.removed, dir)
	return os.RemoveAll(dir)
}

type fakeWriter struct {
	f         *fakeFiles
	base, dir string
}

func (w *fakeWriter) WriteEntry(hdr *tar.Header, rel string, r io.Reader) error {
	if w.f.written == nil {
		w.f.written = map[string]tar.Header{}
	}
	rel, err := filepath.Rel(w.base, filepath.Join(w.dir, rel))
	if err != nil {
		return err
	}
	w.f.written[filepath.ToSlash(rel)] = *hdr
	if r != nil {
		_, err = io.Copy(io.Discard, r)
	}
	return err
}

func (w *fakeWriter) Close() error { return nil }
func (w *fakeWriter) Abort()       {}

// A postgres stack: the server (uid 1000) wrote the database snapshot,
// the stack directory and its compose file; the container wrote its data
// directory as uid 999, and a root-owned compose override was edited by
// hand.
var postgresStack = []ownedEntry{
	{name: "dockmesh.db", uid: 1000, gid: 1000, mode: 0o644},
	{name: "stacks/pg", uid: 1000, gid: 1000, mode: 0o750, dir: true},
	{name: "stacks/pg/compose.override.yaml", uid: 0, gid: 0, mode: 0o644},
	{name: "stacks/pg/conf", uid: 1000, gid: 1000, mode: 0o755, dir: true},
	{name: "stacks/pg/conf/pg.conf", uid: 1000, gid: 1000, mode: 0o644},
	{name: "stacks/pg/data", uid: 999, gid: 999, mode: 0o700, dir: true},
	{name: "stacks/pg/data/base", uid: 999, gid: 999, mode: 0o700, dir: true},
	{name: "stacks/pg/data/base/1", uid: 999, gid: 999, mode: 0o600},
	{name: "stacks/pg/data/PG_VERSION", uid: 999, gid: 999, mode: 0o600},
	{name: "stacks/web", uid: 1000, gid: 1000, mode: 0o750, dir: true},
	{name: "stacks/web/html", uid: 0, gid: 0, mode: 0o755, dir: true},
	{name: "data/secrets.env", uid: 0, gid: 0, mode: 0o600},
}

func TestExtractSendsOtherOwnersThroughHelper(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root restores owners itself")
	}
	cfg, _ := restoreEnv(t)
	files := &fakeFiles{}
	counts, err := Extract(context.Background(), ownedArchive(t, postgresStack...), cfg, Options{Force: true, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"pg/data": 999, "pg/data/base": 999, "pg/data/base/1": 999, "pg/data/PG_VERSION": 999, "web/html": 0}
	if len(files.written) != len(want) {
		t.Errorf("helper wrote %v, want %v", keys(files.written), want)
	}
	for rel, uid := range want {
		hdr, ok := files.written[rel]
		if !ok {
			t.Errorf("%s did not go through the helper", rel)
			continue
		}
		if hdr.Uid != uid || hdr.Gid != uid {
			t.Errorf("%s: owner %d:%d, want %d", rel, hdr.Uid, hdr.Gid, uid)
		}
	}
	if hdr := files.written["pg/data"]; hdr.Mode&0o777 != 0o700 {
		t.Errorf("pg/data: mode %#o, want 0700", hdr.Mode)
	}
	if got := files.dirs; len(got) != 2 || got[0] != filepath.Join(cfg.StacksRoot, "pg") || got[1] != filepath.Join(cfg.StacksRoot, "web") {
		t.Errorf("helper dirs %v, want one per stack", got)
	}
	for _, rel := range []string{"pg/compose.override.yaml", "pg/conf/pg.conf"} {
		if _, err := os.Stat(filepath.Join(cfg.StacksRoot, rel)); err != nil {
			t.Errorf("%s not written by the service: %v", rel, err)
		}
	}
	if counts.LostOwners != 0 {
		t.Errorf("LostOwners = %d, want 0", counts.LostOwners)
	}
	if counts.Files != 6 {
		t.Errorf("Files = %d, want 6", counts.Files)
	}
}

func TestExtractWithoutHelperCountsLostOwners(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root restores owners itself")
	}
	cfg, _ := restoreEnv(t)
	counts, err := Extract(context.Background(), ownedArchive(t, postgresStack...), cfg, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if counts.LostOwners != 5 {
		t.Errorf("LostOwners = %d, want 5", counts.LostOwners)
	}
	fi, err := os.Stat(filepath.Join(cfg.StacksRoot, "pg", "data"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("pg/data: mode %#o, want 0700", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(cfg.StacksRoot, "pg", "data", "PG_VERSION")); err != nil {
		t.Error(err)
	}
}

// The server's database snapshot names the owner; Dockmesh writes it
// first. Entries before it count as the service's own.
func TestExtractBeforeSnapshotKeepsAllOwn(t *testing.T) {
	cfg, _ := restoreEnv(t)
	files := &fakeFiles{}
	entries := append(append([]ownedEntry{}, postgresStack[1:]...), postgresStack[0])
	if _, err := Extract(context.Background(), ownedArchive(t, entries...), cfg, Options{Force: true, Files: files}); err != nil {
		t.Fatal(err)
	}
	if len(files.written) != 0 {
		t.Errorf("helper wrote %v, want nothing", keys(files.written))
	}
}

func TestDiscardStagedRemovesOtherOwnersThroughHelper(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may delete anything")
	}
	cfg, _ := restoreEnv(t)
	p := pathsFor(cfg)
	if err := os.MkdirAll(filepath.Join(p.stacks, "pg", "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(p, &StagedInfo{Source: "test"}); err != nil {
		t.Fatal(err)
	}
	// A directory the service may not empty stands for container data.
	locked := filepath.Join(p.stacks, "pg")
	if err := os.WriteFile(filepath.Join(locked, "data", "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	files := &fakeFiles{}
	err := DiscardStaged(context.Background(), cfg, files)
	if len(files.removed) != 1 || files.removed[0] != p.stacks {
		t.Fatalf("helper removed %v (err %v), want %s", files.removed, err, p.stacks)
	}
}

func keys(m map[string]tar.Header) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The service's own files never come back writable for others; data a
// container wrote keeps its exact mode.
func TestExtractNarrowsOwnWritableModes(t *testing.T) {
	cfg, _ := restoreEnv(t)
	files := &fakeFiles{}
	_, err := Extract(context.Background(), ownedArchive(t,
		ownedEntry{name: "dockmesh.db", uid: 1000, mode: 0o666},
		ownedEntry{name: "stacks/app", uid: 1000, mode: 0o777, dir: true},
		ownedEntry{name: "stacks/app/compose.yaml", uid: 1000, mode: 0o666},
		ownedEntry{name: "stacks/app/shared", uid: 999, mode: 0o777, dir: true},
	), cfg, Options{Force: true, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]os.FileMode{"app": 0o755, "app/compose.yaml": 0o644} {
		fi, err := os.Stat(filepath.Join(cfg.StacksRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: %#o, want %#o", rel, fi.Mode().Perm(), want)
		}
	}
	if fi, err := os.Stat(cfg.DBPath); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("dockmesh.db: %v %v, want 0644", fi.Mode(), err)
	}
	if hdr := files.written["app/shared"]; hdr.Mode&0o777 != 0o777 {
		t.Errorf("container dir: %#o, want 0777", hdr.Mode)
	}
}

func TestExtractRefusesReturningToAStack(t *testing.T) {
	cfg, _ := restoreEnv(t)
	_, err := Extract(context.Background(), ownedArchive(t,
		ownedEntry{name: "dockmesh.db", uid: 1000, mode: 0o644},
		ownedEntry{name: "stacks/a/x", uid: 999, mode: 0o600},
		ownedEntry{name: "stacks/b/y", uid: 999, mode: 0o600},
		ownedEntry{name: "stacks/a/z", uid: 999, mode: 0o600},
	), cfg, Options{Force: true, Files: &fakeFiles{}})
	if err == nil {
		t.Fatal("archive that returns to a stack was accepted")
	}
}

// A directory swapped for a symlink before its mode is set is refused,
// not followed.
func TestDirModeRefusesSwappedDirectory(t *testing.T) {
	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, d := range []string{"a", "other"} {
		if err := os.Mkdir(filepath.Join(base, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := root.Lstat("a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(base, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Join(base, "a")); err != nil {
		t.Fatal(err)
	}
	d := dirMode{fsys: rootFS(root), name: "a", fi: fi, mode: 0o777}
	if err := d.apply(); err == nil {
		t.Fatal("chmod followed the swapped-in symlink")
	}
	if st, err := os.Stat(filepath.Join(base, "other")); err != nil || st.Mode().Perm() != 0o750 {
		t.Errorf("link target changed: %v %v", st.Mode(), err)
	}
}

// A marker naming other paths than ApplyStaged writes is refused before
// anything moves.
func TestApplyStagedRefusesForeignMarkerPaths(t *testing.T) {
	cfg, _ := restoreEnv(t)
	p := pathsFor(cfg)
	dataDir := filepath.Dir(cfg.DBPath)
	pre := filepath.Join(dataDir, PreRestoreDir)
	for _, prev := range []string{
		filepath.Join(pre, "x", "sudoers.d"),
		filepath.Join(pre, "..", "..", ".."),
		filepath.Join(pre, "not-a-stamp"),
		pre + "/x/../20261117-120000",
		dataDir + "/evil/../" + PreRestoreDir + "/20261117-120000",
	} {
		if err := os.MkdirAll(p.root, 0o700); err != nil {
			t.Fatal(err)
		}
		info := &StagedInfo{Source: "test", PrevDir: prev, PrevStacks: cfg.StacksRoot + PreRestoreDir + "-20261117-120000"}
		if err := writeMarker(p, info); err != nil {
			t.Fatal(err)
		}
		if _, err := ApplyStaged(cfg); err == nil {
			t.Errorf("marker with %q accepted", prev)
		}
	}
}
