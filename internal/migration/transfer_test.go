package migration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"

	"github.com/dockmesh/dockmesh/internal/stacks"
)

// fakeVolumes is a host's volumes, each held as the tar.gz archive an
// export would produce.
type fakeVolumes struct {
	mu   sync.Mutex
	vols map[string]*fakeVolume
	// importErr fails every import after reading the archive.
	importErr error
	// rewrite changes an imported archive before it is stored.
	rewrite func([]byte) []byte
	removed []string
}

type fakeVolume struct {
	labels map[string]string
	data   []byte
}

func newFakeVolumes() *fakeVolumes { return &fakeVolumes{vols: map[string]*fakeVolume{}} }

// hasPG reports whether the db stack's volume exists.
func (v *fakeVolumes) hasPG() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.vols["db_pgdata"]
	return ok
}

func (f *fakeHost) InspectVolume(_ context.Context, name string) (volume.Volume, error) {
	f.vols.mu.Lock()
	defer f.vols.mu.Unlock()
	if v, ok := f.vols.vols[name]; ok {
		return volume.Volume{Name: name, Driver: "local", Labels: v.labels}, nil
	}
	return volume.Volume{}, fmt.Errorf("Error response from daemon: get %s: no such volume", name)
}

// CreateVolume returns an existing volume of that name unchanged, as
// Docker does.
func (f *fakeHost) CreateVolume(_ context.Context, name, _ string, labels map[string]string) (volume.Volume, error) {
	f.vols.mu.Lock()
	defer f.vols.mu.Unlock()
	if v, ok := f.vols.vols[name]; ok {
		return volume.Volume{Name: name, Labels: v.labels}, nil
	}
	f.vols.vols[name] = &fakeVolume{labels: labels}
	return volume.Volume{Name: name, Labels: labels}, nil
}

func (f *fakeHost) RemoveVolume(_ context.Context, name string, _ bool) error {
	f.mu.Lock()
	for _, c := range f.containers {
		for _, mt := range c.Mounts {
			if mt.Name == name {
				f.mu.Unlock()
				return fmt.Errorf("Error response from daemon: remove %s: volume is in use - [%s]", name, c.ID)
			}
		}
	}
	f.mu.Unlock()
	f.vols.mu.Lock()
	defer f.vols.mu.Unlock()
	delete(f.vols.vols, name)
	f.vols.removed = append(f.vols.removed, name)
	return nil
}

func (f *fakeHost) RemoveContainer(_ context.Context, id string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.containers[:0]
	for _, c := range f.containers {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	f.containers = kept
	return nil
}

func (v *fakeVolumes) VolumeTar(_ context.Context, name string) (io.ReadCloser, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	vol, ok := v.vols[name]
	if !ok {
		return nil, fmt.Errorf("no such volume %s", name)
	}
	return io.NopCloser(bytes.NewReader(vol.data)), nil
}

func (v *fakeVolumes) VolumeImport(_ context.Context, name string, src io.Reader, _ bool, _ int64) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.importErr != nil {
		return v.importErr
	}
	vol, ok := v.vols[name]
	if !ok {
		return fmt.Errorf("no such volume %s", name)
	}
	if v.rewrite != nil {
		data = v.rewrite(data)
	}
	vol.data = data
	return nil
}

type archiveEntry struct {
	name     string
	typ      byte
	mode     int64
	uid, gid int
	body     string
	link     string
}

func archive(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Uid: e.uid, Gid: e.gid, Linkname: e.link}
		if e.typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.body != "" {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pgData is a postgres data volume: owned by uid 70, the root 0700.
func pgData(t *testing.T) []byte {
	return archive(t,
		archiveEntry{name: "./", typ: tar.TypeDir, mode: 0o700, uid: 70, gid: 70},
		archiveEntry{name: "./PG_VERSION", typ: tar.TypeReg, mode: 0o600, uid: 70, gid: 70, body: "17\n"},
		archiveEntry{name: "./base/", typ: tar.TypeDir, mode: 0o700, uid: 70, gid: 70},
		archiveEntry{name: "./base/1", typ: tar.TypeReg, mode: 0o600, uid: 70, gid: 70, body: strings.Repeat("x", 4096)},
	)
}

func TestManifestDiff(t *testing.T) {
	base := []archiveEntry{
		{name: "./", typ: tar.TypeDir, mode: 0o700, uid: 70, gid: 70},
		{name: "./a", typ: tar.TypeReg, mode: 0o600, uid: 70, gid: 70, body: "one"},
		{name: "./b", typ: tar.TypeLink, link: "./a"},
		{name: "./l", typ: tar.TypeSymlink, mode: 0o777, link: "a"},
	}
	with := func(f func([]archiveEntry)) []archiveEntry {
		c := append([]archiveEntry(nil), base...)
		f(c)
		return c
	}
	changed := "of 4 entries, 0 missing, 1 with other contents, owner or mode, 0 extra"
	tests := []struct {
		name    string
		copy    []archiveEntry
		wantErr string
	}{
		{"same", base, ""},
		{"hard link the other way round", []archiveEntry{base[0], {name: "b", typ: tar.TypeReg, mode: 0o600, uid: 70, gid: 70, body: "one"}, {name: "a", typ: tar.TypeLink, link: "b"}, base[3]}, ""},
		{"root owner lost", with(func(c []archiveEntry) { c[0].uid, c[0].gid = 0, 0 }), changed},
		{"root mode lost", with(func(c []archiveEntry) { c[0].mode = 0o755 }), changed},
		{"content", with(func(c []archiveEntry) { c[1].body = "two" }), "of 4 entries, 0 missing, 2 with other contents, owner or mode, 0 extra"},
		{"missing file", base[:3], "of 4 entries, 1 missing, 0 with other contents, owner or mode, 0 extra"},
		{"extra file", append(append([]archiveEntry(nil), base...), archiveEntry{name: "./x", typ: tar.TypeReg, mode: 0o600, body: "x"}), "of 4 entries, 0 missing, 0 with other contents, owner or mode, 1 extra"},
		{"symlink target", with(func(c []archiveEntry) { c[3].link = "x" }), changed},
	}
	src, err := readManifest(bytes.NewReader(archive(t, base...)), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp, err := readManifest(bytes.NewReader(archive(t, tt.copy...)), nil)
			if err != nil {
				t.Fatal(err)
			}
			err = src.diff(cp)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("diff = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// Extended attributes (ACLs, file capabilities) are part of the check.
func TestManifestComparesXattrs(t *testing.T) {
	plain := archive(t, archiveEntry{name: "./f", typ: tar.TypeReg, mode: 0o640, body: "x"})
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "./f", Typeflag: tar.TypeReg, Mode: 0o640, Size: 1, Format: tar.FormatPAX,
		PAXRecords: map[string]string{"SCHILY.xattr.system.posix_acl_access": "acl"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := readManifest(bytes.NewReader(buf.Bytes()), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := readManifest(bytes.NewReader(plain), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.diff(b) == nil {
		t.Fatal("an ACL lost in the copy went unnoticed")
	}
}

func TestManifestRefusesTooManyEntries(t *testing.T) {
	old := maxManifestEntries
	maxManifestEntries = 2
	t.Cleanup(func() { maxManifestEntries = old })
	if _, err := readManifest(bytes.NewReader(pgData(t)), nil); !errors.Is(err, errTooManyEntries) {
		t.Fatalf("err = %v", err)
	}
}

// A cut-off archive is an error, never a smaller volume.
func TestManifestRefusesTruncatedArchive(t *testing.T) {
	data := pgData(t)
	if _, err := readManifest(bytes.NewReader(data[:len(data)-10]), nil); err == nil {
		t.Fatal("truncated archive read without error")
	}
}

// volumeEnv is a migration of stack db, whose postgres data lives in the
// named volume db_pgdata on the source.
func volumeEnv(t *testing.T) *migrationEnv {
	t.Helper()
	e := newMigrationEnv(t)
	if _, err := e.mgr.Create("db", "services:\n  db:\n    image: postgres\n    volumes:\n      - pgdata:/var/lib/postgresql/data\nvolumes:\n  pgdata: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.deployments.Set(context.Background(), "db", "agent0", "deployed"); err != nil {
		t.Fatal(err)
	}
	e.source.vols, e.target.vols = newFakeVolumes(), newFakeVolumes()
	e.source.vols.vols["db_pgdata"] = &fakeVolume{labels: map[string]string{"com.docker.compose.project": "db", "com.docker.compose.volume": "pgdata"}, data: pgData(t)}
	e.svc.SetVolumeIO(func(id string) (VolumeIO, error) {
		switch id {
		case "agent0":
			return e.source.vols, nil
		case "agent1":
			return e.target.vols, nil
		}
		return nil, errors.New("unknown host")
	})
	return e
}

// migrateDB runs a migration of stack db to the end.
func (e *migrationEnv) migrateDB(t *testing.T) *Migration {
	t.Helper()
	m, err := e.svc.Initiate(context.Background(), "db", "agent1", "u1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return e.wait(t, m.ID)
}

func TestMigrationCopiesNamedVolume(t *testing.T) {
	e := volumeEnv(t)
	m := e.migrateDB(t)
	if m.Status != StatusCompleted {
		t.Fatalf("status %s: %s", m.Status, m.ErrorMessage)
	}
	e.target.vols.mu.Lock()
	got := e.target.vols.vols["db_pgdata"]
	e.target.vols.mu.Unlock()
	if got == nil || !bytes.Equal(got.data, e.source.vols.vols["db_pgdata"].data) {
		t.Fatal("volume data not on the target")
	}
	if got.labels["com.docker.compose.volume"] != "pgdata" {
		t.Fatalf("target volume labels %v", got.labels)
	}
	if !e.source.vols.hasPG() {
		t.Fatal("the source's volume is gone")
	}
	p := m.Progress
	if p == nil || len(p.VolumesCopied) != 1 || p.VolumesCopied[0].Bytes != 4099 || p.BytesDone != 4099 || p.VolumesTotal != 1 {
		t.Fatalf("progress %+v", p)
	}
}

// A failed or mismatching copy restores the stack on the source and
// removes what the migration created on the target.
func TestMigrationVolumeFailureRollsBack(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*migrationEnv)
		want  string
	}{
		{"target disk full", func(e *migrationEnv) {
			e.target.vols.importErr = errors.New("not enough free space in the volume")
		}, "not enough free space"},
		{"owner lost on the target", func(e *migrationEnv) {
			e.target.vols.rewrite = func([]byte) []byte {
				return archive(t,
					archiveEntry{name: "./", typ: tar.TypeDir, mode: 0o700},
					archiveEntry{name: "./PG_VERSION", typ: tar.TypeReg, mode: 0o600, uid: 70, gid: 70, body: "17\n"},
					archiveEntry{name: "./base/", typ: tar.TypeDir, mode: 0o700, uid: 70, gid: 70},
					archiveEntry{name: "./base/1", typ: tar.TypeReg, mode: 0o600, uid: 70, gid: 70, body: strings.Repeat("x", 4096)},
				)
			}
		}, "1 with other contents, owner or mode"},
		{"file missing on the target", func(e *migrationEnv) {
			e.target.vols.rewrite = func([]byte) []byte {
				return archive(t, archiveEntry{name: "./", typ: tar.TypeDir, mode: 0o700, uid: 70, gid: 70})
			}
		}, "3 missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := volumeEnv(t)
			tt.setup(e)
			m := e.migrateDB(t)
			if m.Status != StatusRolledBack || !strings.Contains(m.ErrorMessage, tt.want) {
				t.Fatalf("status %s: %s", m.Status, m.ErrorMessage)
			}
			if e.target.vols.hasPG() {
				t.Fatal("volume left on the target")
			}
			if !e.source.isDeployed() || !e.source.vols.hasPG() {
				t.Fatal("stack not restored on the source with its volume")
			}
			p := m.Progress
			if p.FailedPhase != StatusSyncing || strings.Join(p.Undo, ",") != UndoRemoveVolumes+","+UndoRestartSource {
				t.Fatalf("progress %+v", p)
			}
		})
	}
}

// A volume of that name on the target is never overwritten; the
// migration fails before the source is stopped.
func TestMigrationRefusesExistingTargetVolume(t *testing.T) {
	e := volumeEnv(t)
	e.target.vols.vols["db_pgdata"] = &fakeVolume{data: []byte("theirs")}
	m := e.migrateDB(t)
	if m.Status != StatusFailed || !strings.Contains(m.ErrorMessage, "already exists on the target") {
		t.Fatalf("status %s: %s", m.Status, m.ErrorMessage)
	}
	if e.source.stops != 0 || string(e.target.vols.vols["db_pgdata"].data) != "theirs" {
		t.Fatal("source stopped or target volume touched")
	}
}

func TestPreflightRefusesVolumeDrivers(t *testing.T) {
	e := volumeEnv(t)
	if _, err := e.mgr.Create("nfs", "services:\n  app:\n    image: nginx\n    volumes:\n      - share:/data\nvolumes:\n  share:\n    driver_opts:\n      type: nfs\n      device: \":/export\"\n", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.deployments.Set(context.Background(), "nfs", "agent0", "deployed"); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Preflight(context.Background(), "nfs", "agent1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Checks {
		if c.Name == "named_volumes" && c.Status == CheckFail && strings.Contains(c.Detail, "driver options") {
			return
		}
	}
	t.Fatalf("checks %+v", res.Checks)
}

// Rolling back a completed migration leaves the target's copy: it holds
// what the stack wrote there since.
func TestRollbackOfCompletedMigrationKeepsTargetVolumes(t *testing.T) {
	e := volumeEnv(t)
	m := e.migrateDB(t)
	if m.Status != StatusCompleted {
		t.Fatalf("status %s: %s", m.Status, m.ErrorMessage)
	}
	if err := e.svc.Rollback(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	m = e.wait(t, m.ID)
	if m.Status != StatusRolledBack || !e.target.vols.hasPG() {
		t.Fatalf("status %s, target volume kept %v", m.Status, e.target.vols.hasPG())
	}
	if !strings.Contains(strings.Join(m.Progress.Undo, ","), UndoKeepVolumes) {
		t.Fatalf("undo %v", m.Progress.Undo)
	}
}

// A server restart during the copy removes the half-copied volume and
// restores the source; a second start changes nothing more.
func TestRestartDuringCopyCleansTarget(t *testing.T) {
	e := volumeEnv(t)
	ctx := context.Background()
	e.target.vols.vols["db_pgdata"] = &fakeVolume{labels: map[string]string{labelMigration: "m1"}, data: []byte("partial")}
	m := &Migration{ID: "m1", StackName: "db", SourceHostID: "agent0", TargetHostID: "agent1", Status: StatusSyncing, Phase: StatusSyncing,
		Progress: &Progress{CreatedVolumes: []string{"db_pgdata"}}}
	if err := e.svc.store.Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	e.source.deployed = false
	for i := 0; i < 2; i++ {
		svc := NewService(e.db, fakeHosts{"agent0": e.source, "agent1": e.target}, e.mgr, stacks.NewDeploymentStore(e.db))
		if err := svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		svc.Recover(ctx)
		e.svc = svc
		got := e.wait(t, "m1")
		if got.Status != StatusRolledBack {
			t.Fatalf("start %d: status %s: %s", i+1, got.Status, got.ErrorMessage)
		}
	}
	e.target.vols.mu.Lock()
	removed := len(e.target.vols.removed)
	e.target.vols.mu.Unlock()
	if e.target.vols.hasPG() || removed != 1 || !e.source.isDeployed() {
		t.Fatalf("target volume %v, removed %v, source deployed %v", e.target.vols.hasPG(), e.target.vols.removed, e.source.isDeployed())
	}
}

// Finalising removes the source's copy, unless the stack runs there again.
func TestPurgeSourceRemovesCopiedVolumes(t *testing.T) {
	e := volumeEnv(t)
	ctx := context.Background()
	m := e.migrateDB(t)
	if m.Status != StatusCompleted {
		t.Fatalf("status %s: %s", m.Status, m.ErrorMessage)
	}
	if err := e.svc.deployments.Set(ctx, "db", "agent0", "deployed"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PurgeSource(ctx, m.ID); !errors.Is(err, ErrStackOnSource) {
		t.Fatalf("purge with the stack back on the source: %v", err)
	}
	if !e.source.vols.hasPG() {
		t.Fatal("source volume removed")
	}
	if err := e.svc.deployments.Set(ctx, "db", "agent1", "deployed"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.PurgeSource(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if e.source.vols.hasPG() || !e.target.vols.hasPG() {
		t.Fatal("purge did not remove only the source's copy")
	}
}

// A volume that shows up on the target while earlier ones copy is
// neither written to nor removed.
func TestMigrationLeavesVolumeCreatedMeanwhile(t *testing.T) {
	e := volumeEnv(t)
	e.target.vols.vols["db_pgdata"] = &fakeVolume{labels: map[string]string{"owner": "someone"}, data: []byte("theirs")}
	m := &Migration{ID: "m1", StackName: "db", SourceHostID: "agent0", TargetHostID: "agent1"}
	err := e.svc.copyVolumes(context.Background(), m, e.target, []volumeToCopy{{name: "db_pgdata"}})
	if err == nil || !strings.Contains(err.Error(), "appeared on the target") {
		t.Fatalf("err = %v", err)
	}
	if len(m.Progress.CreatedVolumes) != 0 {
		t.Fatalf("recorded %v as created", m.Progress.CreatedVolumes)
	}
	if left := e.svc.removeCreatedVolumes(context.Background(), &Migration{ID: "m1", Progress: &Progress{CreatedVolumes: []string{"db_pgdata"}}}, e.target); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	if string(e.target.vols.vols["db_pgdata"].data) != "theirs" {
		t.Fatal("their volume was touched")
	}
}

// A commit whose status write was lost is completed on the next start,
// never rolled back over the target's data; twice changes nothing more.
func TestStartCompletesCommittedMigration(t *testing.T) {
	e := volumeEnv(t)
	ctx := context.Background()
	e.target.vols.vols["db_pgdata"] = &fakeVolume{labels: map[string]string{labelMigration: "m1"}, data: []byte("written since")}
	m := &Migration{ID: "m1", StackName: "db", SourceHostID: "agent0", TargetHostID: "agent1", Status: StatusCommitting, Phase: StatusCommitting,
		Progress: &Progress{CreatedVolumes: []string{"db_pgdata"}}}
	if err := e.svc.store.Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.deployments.Set(ctx, "db", "agent1", "deployed"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		svc := NewService(e.db, fakeHosts{"agent0": e.source, "agent1": e.target}, e.mgr, stacks.NewDeploymentStore(e.db))
		if err := svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		svc.Recover(ctx)
		e.svc = svc
		if got := e.wait(t, "m1"); got.Status != StatusCompleted {
			t.Fatalf("start %d: status %s: %s", i+1, got.Status, got.ErrorMessage)
		}
	}
	if !e.target.vols.hasPG() || e.source.stops != 0 {
		t.Fatal("target volume removed or source touched")
	}
}

// Once finalised, a migration can't be rolled back onto the emptied source.
func TestRollbackRefusedAfterPurge(t *testing.T) {
	e := volumeEnv(t)
	m := e.migrateDB(t)
	if m.Status != StatusCompleted {
		t.Fatalf("status %s: %s", m.Status, m.ErrorMessage)
	}
	if err := e.svc.PurgeSource(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Rollback(context.Background(), m.ID); !errors.Is(err, ErrPurged) {
		t.Fatalf("rollback after purge: %v", err)
	}
}

// A volume another stack created under the name is not this stack's data.
func TestPreflightRefusesOtherStacksVolume(t *testing.T) {
	e := volumeEnv(t)
	e.source.vols.vols["db_pgdata"].labels["com.docker.compose.project"] = "other"
	res, err := e.svc.Preflight(context.Background(), "db", "agent1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Checks {
		if c.Name == "named_volumes" && c.Status == CheckFail && strings.Contains(c.Detail, "belongs to another stack") {
			return
		}
	}
	t.Fatalf("checks %+v", res.Checks)
}

// A deploy on the target while the rollback waited for the source owns the
// copied volumes; the rollback then leaves both hosts alone.
func TestRollbackLeavesStackDeployedOnTargetMeanwhile(t *testing.T) {
	e := volumeEnv(t)
	ctx := context.Background()
	e.target.vols.vols["db_pgdata"] = &fakeVolume{labels: map[string]string{labelMigration: "m1"}, data: []byte("written since")}
	m := &Migration{ID: "m1", StackName: "db", SourceHostID: "agent0", TargetHostID: "agent1", Status: StatusHealthCheck, Phase: StatusHealthCheck,
		Progress: &Progress{CreatedVolumes: []string{"db_pgdata"}}}
	if err := e.svc.store.Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.deployments.Set(ctx, "db", "agent1", "deployed"); err != nil {
		t.Fatal(err)
	}
	e.svc.rollback(ctx, m, true, "phase health_check: test")
	got := e.wait(t, "m1")
	if got.Status != StatusFailed || !strings.Contains(got.ErrorMessage, "nothing undone") {
		t.Fatalf("status %s: %s", got.Status, got.ErrorMessage)
	}
	if !e.target.vols.hasPG() || e.target.stops != 0 || len(e.source.deploys) != 0 {
		t.Fatal("rollback touched the stack deployed on the target")
	}
}

// A helper left behind by an agent killed mid-copy doesn't keep the
// rollback from removing the volume; a user's container does.
func TestRollbackRemovesVolumeHeldByStoppedHelper(t *testing.T) {
	e := volumeEnv(t)
	ctx := context.Background()
	e.target.vols.vols["db_pgdata"] = &fakeVolume{labels: map[string]string{labelMigration: "m1"}}
	mounts := []container.MountPoint{{Type: mount.TypeVolume, Name: "db_pgdata"}}
	e.target.containers = []container.Summary{{ID: "helper", State: container.StateExited, Labels: map[string]string{"dockmesh.component": "backup-helper"}, Mounts: mounts}}
	m := &Migration{ID: "m1", Progress: &Progress{CreatedVolumes: []string{"db_pgdata"}}}
	if left := e.svc.removeCreatedVolumes(ctx, m, e.target); len(left) != 0 || e.target.vols.hasPG() {
		t.Fatalf("left %v", left)
	}

	e.target.vols.vols["db_pgdata"] = &fakeVolume{labels: map[string]string{labelMigration: "m1"}}
	e.target.containers = []container.Summary{{ID: "app", State: container.StateExited, Mounts: mounts}}
	old := targetWaitMax
	targetWaitMax = 0
	t.Cleanup(func() { targetWaitMax = old })
	m.Progress.CreatedVolumes = []string{"db_pgdata"}
	if left := e.svc.removeCreatedVolumes(ctx, m, e.target); len(left) != 1 || !e.target.vols.hasPG() {
		t.Fatalf("left %v, volume kept %v", left, e.target.vols.hasPG())
	}
}
