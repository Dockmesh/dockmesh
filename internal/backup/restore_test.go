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
	"testing"

	"github.com/dockmesh/dockmesh/internal/stacks"
)

// importHost records what a restore wrote, per volume.
type importHost struct {
	*fakeHost
	imported map[string][]byte
	replaced map[string]bool
	inUse    map[string][]string
	picked   []string
}

func newImportHost(f *fakeHost) *importHost {
	return &importHost{fakeHost: f, imported: map[string][]byte{}, replaced: map[string]bool{}, inUse: map[string][]string{}}
}

func (h *importHost) Pick(id string) (hostBackupTarget, error) {
	h.picked = append(h.picked, id)
	return h, nil
}

func (h *importHost) VolumeImport(_ context.Context, name string, src io.Reader, replace bool) error {
	b, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	if _, err := walkInnerTar(bytes.NewReader(b)); err != nil {
		return err
	}
	h.imported[name], h.replaced[name] = b, replace
	return nil
}

func (h *importHost) VolumeInUse(_ context.Context, name string) ([]string, error) {
	return h.inUse[name], nil
}

func TestRestoreVolume(t *testing.T) {
	svc, fake, root := newExecTest(t, true)
	host := newImportHost(fake)
	svc.SetHostResolver(host)
	ctx := context.Background()
	in := volumeJob("v", root)
	in.Encrypt = true
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(ctx, job.ID)
	if err != nil || run.Status != StatusSuccess {
		t.Fatalf("%v %+v", err, run)
	}

	// Defaults: the backed-up volume, on the host it came from.
	host.picked = nil
	rep, err := svc.Restore(ctx, run.ID, RestoreOptions{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.HostID != "fake" || host.picked[len(host.picked)-1] != "fake" || !host.replaced["data"] {
		t.Fatalf("restore went to %v (report %+v)", host.picked, rep)
	}
	if !bytes.Equal(host.imported["data"], fake.volumes["data"]) {
		t.Fatal("restored bytes differ from the backed-up volume")
	}

	host.inUse["data"] = []string{"db-1"}
	var inUse *VolumeInUseError
	if _, err := svc.Restore(ctx, run.ID, RestoreOptions{Replace: true}); !errors.As(err, &inUse) {
		t.Fatalf("replace of a volume in use: %v", err)
	}
	if _, err := svc.Restore(ctx, run.ID, RestoreOptions{Target: "copy"}); err != nil {
		t.Fatalf("merge into another volume: %v", err)
	}
	if _, err := svc.Restore(ctx, run.ID, RestoreOptions{Target: "/etc"}); err == nil {
		t.Fatal("path accepted as volume name")
	}

	// A damaged archive is refused before anything is written.
	p := filepath.Join(root, filepath.FromSlash(run.TargetPath))
	data, _ := os.ReadFile(p)
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Restore(ctx, run.ID, RestoreOptions{Target: "fresh"}); err == nil {
		t.Fatal("damaged archive restored")
	}
	if _, ok := host.imported["fresh"]; ok {
		t.Fatal("damaged archive reached the volume")
	}
}

func TestRestoreStack(t *testing.T) {
	svc, fake, root := newExecTest(t, false)
	stacksRoot := t.TempDir()
	mgr, err := stacks.NewManager(stacksRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.stacks = mgr
	host := newImportHost(fake)
	svc.SetHostResolver(host)
	ctx := context.Background()

	compose := "services:\n  db:\n    image: postgres\n    volumes: [\"pg:/var/lib/postgresql/data\"]\nvolumes:\n  pg: {}\n"
	vol := tarGz(t, map[string]string{"PG_VERSION": "16"})
	archive := stackArchive(t, compose, map[string][]byte{"shop_pg": vol})
	run := storeRun(ctx, t, svc, root, archive)

	rep, err := svc.Restore(ctx, run, RestoreOptions{Target: "shop-copy", Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(stacksRoot, "shop-copy", "compose.yaml"))
	if err != nil || string(got) != compose {
		t.Fatalf("compose not restored: %v", err)
	}
	// Restored as another stack: the copy gets its own volume, the
	// original's stays untouched.
	if !bytes.Equal(host.imported["shop-copy_pg"], vol) || len(rep.VolumesRestored) != 1 {
		t.Fatalf("volume not restored as shop-copy_pg: %+v", rep)
	}
	if _, ok := host.imported["shop_pg"]; ok {
		t.Fatal("restoring as a copy wrote into the original stack's volume")
	}

	// The archive misses a volume its manifest lists: nothing is written.
	broken := stackArchiveManifest(t, compose, []string{"shop_pg", "shop_cache"}, map[string][]byte{"shop_pg": vol})
	brokenRun := storeRun(ctx, t, svc, root, broken)
	if _, err := svc.Restore(ctx, brokenRun, RestoreOptions{Target: "shop-two"}); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("incomplete archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stacksRoot, "shop-two")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stack dir written for an archive that failed verification")
	}
}

func stackArchive(t *testing.T, compose string, volumes map[string][]byte) []byte {
	names := make([]string, 0, len(volumes))
	for n := range volumes {
		names = append(names, n)
	}
	return stackArchiveManifest(t, compose, names, volumes)
}

func stackArchiveManifest(t *testing.T, compose string, manifest []string, volumes map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, body []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	m := `{"format":1,"stack":"shop","volumes":[`
	for i, v := range manifest {
		if i > 0 {
			m += ","
		}
		m += `"` + v + `"`
	}
	add(stackManifestName, []byte(m+"]}"))
	add("stack/compose.yaml", []byte(compose))
	for n, v := range volumes {
		add("volumes/"+n+".tar.gz", v)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

var storedRuns int64

// storeRun puts an archive of stack shop on the job's target and records a
// successful run for it, as if a backup had produced it.
func storeRun(ctx context.Context, t *testing.T, svc *Service, root string, archive []byte) int64 {
	t.Helper()
	storedRuns++
	in := volumeJob("job-shop-"+itoa(storedRuns), root)
	in.Sources = []Source{{Type: "stack", Name: "shop"}}
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.exec.begin(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	path := "jobs/" + itoa(job.ID) + "/a.tar.gz"
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.finishRun(ctx, id, StatusSuccess, int64(len(archive)), path, sha256Hex(archive), nil); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRestoreGuards(t *testing.T) {
	svc, fake, root := newExecTest(t, false)
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.stacks = mgr
	host := newImportHost(fake)
	svc.SetHostResolver(host)
	ctx := context.Background()
	compose := "services:\n  db:\n    image: postgres\n"
	vol := tarGz(t, map[string]string{"f": "1"})

	t.Run("copy leaves fixed-name volumes alone", func(t *testing.T) {
		run := storeRun(ctx, t, svc, root, stackArchive(t, compose, map[string][]byte{"shop_pg": vol, "shared-certs": vol}))
		rep, err := svc.Restore(ctx, run, RestoreOptions{Target: "shop-copy", Replace: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := host.imported["shared-certs"]; ok {
			t.Fatal("a copy restore wrote into a volume with a fixed name")
		}
		if len(rep.VolumesRestored) != 1 || len(rep.Warnings) != 1 {
			t.Fatalf("report: %+v", rep)
		}
	})

	t.Run("busy stack or volume", func(t *testing.T) {
		release, err := svc.claimRestore([]string{"stack:shop"})
		if err != nil {
			t.Fatal(err)
		}
		run := storeRun(ctx, t, svc, root, stackArchive(t, compose, map[string][]byte{"shop_pg": vol}))
		if _, err := svc.Restore(ctx, run, RestoreOptions{}); !errors.Is(err, ErrRestoreBusy) {
			t.Fatalf("restore while another one runs: %v", err)
		}
		release()
		if _, err := svc.Restore(ctx, run, RestoreOptions{}); err != nil {
			t.Fatalf("restore after the other one ended: %v", err)
		}
	})

	t.Run("replace needs a recorded checksum", func(t *testing.T) {
		run := storeRun(ctx, t, svc, root, stackArchive(t, compose, map[string][]byte{"shop_pg": vol}))
		if _, err := svc.store.db.Exec(`UPDATE backup_runs SET sha256 = '' WHERE id = ?`, run); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Restore(ctx, run, RestoreOptions{Replace: true}); !errors.Is(err, ErrUnverifiedReplace) {
			t.Fatalf("replace from an unhashed run: %v", err)
		}
		if _, err := svc.Restore(ctx, run, RestoreOptions{}); err != nil {
			t.Fatalf("merge from an unhashed run: %v", err)
		}
	})
}
