package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// fakeHost serves volume archives from memory and records hook calls.
type fakeHost struct {
	volumes  map[string][]byte
	failVol  map[string]error
	hookExit map[string]int
	calls    []string
}

func (f *fakeHost) Pick(string) (hostBackupTarget, error) { return f, nil }

func (f *fakeHost) VolumeTar(_ context.Context, name string) (io.ReadCloser, error) {
	if tarErr, failing := f.failVol[name]; failing {
		// Half an archive, then the error a dying tar would produce.
		data := f.volumes[name]
		return io.NopCloser(io.MultiReader(bytes.NewReader(data[:len(data)/2]), errReader{tarErr})), nil
	}
	data, ok := f.volumes[name]
	if !ok {
		return nil, errors.New("no such volume: " + name)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeHost) ContainerExec(_ context.Context, container string, cmd []string) ([]byte, int, error) {
	f.calls = append(f.calls, container+":"+strings.Join(cmd, " "))
	return nil, f.hookExit[container], nil
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body := files[n]
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newExecTest returns a service whose local target writes under a temp
// dir and whose jobs run against fake.
func newExecTest(t *testing.T, encrypt bool) (*Service, *fakeHost, string) {
	t.Helper()
	svc, _ := newTestService(t, encrypt)
	root := t.TempDir()
	targets.SetLocalRoots(func() []string { return []string{root} })
	t.Cleanup(func() { targets.SetLocalRoots(nil) })
	fake := &fakeHost{
		volumes:  map[string][]byte{"data": tarGz(t, map[string]string{"a.txt": strings.Repeat("hello ", 5000)})},
		failVol:  map[string]error{},
		hookExit: map[string]int{},
	}
	svc.SetHostResolver(fake)
	return svc, fake, root
}

func volumeJob(name, root string) JobInput {
	return JobInput{
		Name:         name,
		HostID:       "fake",
		TargetType:   "local",
		TargetConfig: map[string]any{"path": root},
		Sources:      []Source{{Type: "volume", Name: "data"}},
		Enabled:      true,
	}
}

func archives(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestRetentionKeepsNewestAndIgnoresOtherJobs(t *testing.T) {
	svc, fake, root := newExecTest(t, false)
	ctx := context.Background()

	// A name that prefixes another must not share its archives.
	in := volumeJob("db", root)
	in.RetentionCount = 2
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateJob(ctx, volumeJob("db-prod", root))
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := svc.RunNow(ctx, other.ID)
	if err != nil || otherRun.Status != StatusSuccess {
		t.Fatalf("other job run: %v %+v", err, otherRun)
	}

	var runs []*Run
	for i := 0; i < 4; i++ {
		if i == 2 {
			fake.failVol["data"] = errors.New("tar exit 1")
		}
		r, err := svc.RunNow(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		delete(fake.failVol, "data")
		runs = append(runs, r)
	}
	if runs[2].Status != StatusFailed || runs[2].TargetPath != "" {
		t.Fatalf("failed run should have no archive: %+v", runs[2])
	}
	for _, i := range []int{0, 1, 3} {
		if runs[i].Status != StatusSuccess {
			t.Fatalf("run %d: %+v", i, runs[i])
		}
	}

	got := archives(t, root)
	want := []string{otherRun.TargetPath, runs[1].TargetPath, runs[3].TargetPath}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("archives on target:\n got %v\nwant %v", got, want)
	}
	pruned, _ := svc.GetRun(ctx, runs[0].ID)
	if pruned.PrunedAt == nil || pruned.Restorable() {
		t.Fatalf("oldest run should be pruned: %+v", pruned)
	}
	if !strings.HasPrefix(runs[3].TargetPath, "jobs/"+itoa(job.ID)+"-db/") {
		t.Fatalf("archive path not keyed by job id: %s", runs[3].TargetPath)
	}
}

func TestRetentionCleansLegacyPartials(t *testing.T) {
	svc, _, root := newExecTest(t, false)
	ctx := context.Background()
	in := volumeJob("vol", root)
	in.RetentionCount = 5
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// A failed run that left its file behind, no hash.
	legacy := filepath.Join(root, "jobs", "vol", "old.tar.gz")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.db.Exec(`INSERT INTO backup_runs (job_id, job_name, status, target_path, sources_json)
		VALUES (?, 'vol', 'failed', 'jobs/vol/old.tar.gz', '[]')`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunNow(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy partial archive still there: %v", err)
	}
}

func TestHooks(t *testing.T) {
	cases := []struct {
		name      string
		pre       []Hook
		post      []Hook
		exits     map[string]int
		status    string
		postCalls int
	}{
		{"post-hook failure keeps the archive", nil, []Hook{{Container: "db", Cmd: []string{"start"}}},
			map[string]int{"db": 1}, StatusSuccessWithWarnings, 1},
		{"second pre-hook fails, post-hooks still run",
			[]Hook{{Container: "db", Cmd: []string{"stop"}}, {Container: "bad", Cmd: []string{"dump"}}},
			[]Hook{{Container: "db2", Cmd: []string{"start"}}},
			map[string]int{"bad": 2}, StatusFailed, 1},
		{"first pre-hook fails, nothing to undo",
			[]Hook{{Container: "bad", Cmd: []string{"stop"}}},
			[]Hook{{Container: "db2", Cmd: []string{"start"}}},
			map[string]int{"bad": 2}, StatusFailed, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, root := newExecTest(t, false)
			fake.hookExit = tc.exits
			ctx := context.Background()
			// Hooks are stored directly: the scope checks live in the handler.
			job, err := svc.CreateJob(ctx, volumeJob("hooks", root))
			if err != nil {
				t.Fatal(err)
			}
			job.PreHooks, job.PostHooks = tc.pre, tc.post
			run, err := svc.exec.Run(ctx, job)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != tc.status {
				t.Fatalf("status %s, want %s (%s)", run.Status, tc.status, run.Error)
			}
			posts := 0
			for _, c := range fake.calls {
				if strings.HasPrefix(c, "db:start") || strings.HasPrefix(c, "db2:start") {
					posts++
				}
			}
			if posts != tc.postCalls {
				t.Fatalf("post-hook calls %d, want %d (%v)", posts, tc.postCalls, fake.calls)
			}
			if tc.status == StatusSuccessWithWarnings {
				res, err := svc.VerifyRun(ctx, run.ID)
				if err != nil || !res.Passed {
					t.Fatalf("archive of a run with warnings must verify: %v %+v", err, res)
				}
			}
		})
	}
}

func TestVerifyHash(t *testing.T) {
	for _, encrypt := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "encrypted"}[encrypt], func(t *testing.T) {
			svc, _, root := newExecTest(t, encrypt)
			ctx := context.Background()
			in := volumeJob("v", root)
			in.Encrypt = encrypt
			job, err := svc.CreateJob(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.RunNow(ctx, job.ID)
			if err != nil || run.Status != StatusSuccess {
				t.Fatalf("%v %+v", err, run)
			}
			res, err := svc.VerifyRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Passed || !res.SHA256OK {
				t.Fatalf("verify failed: %+v", res.Checks)
			}

			// Corrupt the stored file: the hash check has to catch it.
			p := filepath.Join(root, filepath.FromSlash(run.TargetPath))
			data, _ := os.ReadFile(p)
			data[len(data)-1] ^= 0xff
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if res, err := svc.VerifyRun(ctx, run.ID); err == nil && res.Passed {
				t.Fatal("corrupted archive passed verify")
			}
		})
	}
}

func TestEncryptedRunHashCoversStoredBytes(t *testing.T) {
	svc, _, root := newExecTest(t, true)
	ctx := context.Background()
	in := volumeJob("v", root)
	in.Encrypt = true
	job, err := svc.CreateJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(run.TargetPath)))
	if err != nil {
		t.Fatal(err)
	}
	if run.SHA256 != sha256Hex(data) {
		t.Fatalf("run sha256 %s, stored file %s", run.SHA256, sha256Hex(data))
	}
	res, err := svc.VerifyRun(ctx, run.ID)
	if err != nil || !res.Passed || !res.SHA256OK {
		t.Fatalf("encrypted run should verify: %v %+v", err, res)
	}
}

func TestDeletedJobRunsStayRestorable(t *testing.T) {
	svc, _, root := newExecTest(t, false)
	ctx := context.Background()
	job, err := svc.CreateJob(ctx, volumeJob("gone", root))
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetJob(ctx, job.ID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("deleted job still listed: %v", err)
	}
	if _, err := svc.CreateJob(ctx, volumeJob("gone", root)); err != nil {
		t.Fatalf("name of a deleted job should be free: %v", err)
	}
	rc, err := svc.ReadRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("run of a deleted job: %v", err)
	}
	rc.Close()

	// A job without archives is removed for good.
	empty, err := svc.CreateJob(ctx, volumeJob("empty", root))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteJob(ctx, empty.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.getJobForRun(ctx, empty.ID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("empty job not removed: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestStackArchiveVolumes(t *testing.T) {
	dir := t.TempDir()
	compose := "services:\n  app:\n    image: nginx\n    volumes: [\"data:/d\", \"cache:/c\"]\nvolumes:\n  data: {}\n  cache: {}\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	vol := tarGz(t, map[string]string{"f": "x"})
	ctx := context.Background()

	fake := &fakeHost{volumes: map[string][]byte{"web_data": vol, "web_cache": vol}}
	var buf bytes.Buffer
	if _, err := tarStackWithVolumes(ctx, fake, dir, "web", &buf); err != nil {
		t.Fatal(err)
	}
	res := &VerifyResult{Passed: true}
	verifyStackArchive(res, bytes.NewReader(buf.Bytes()))
	if !res.Passed || len(res.Counts.Volumes) != 2 {
		t.Fatalf("complete archive: %+v", res)
	}

	// A volume that can't be read fails the backup.
	delete(fake.volumes, "web_cache")
	if _, err := tarStackWithVolumes(ctx, fake, dir, "web", io.Discard); err == nil || !strings.Contains(err.Error(), "web_cache") {
		t.Fatalf("missing volume: %v", err)
	}

	// An archive that lost a volume fails verify.
	var lossy bytes.Buffer
	gz := gzip.NewWriter(&lossy)
	tw := tar.NewWriter(gz)
	manifest := `{"format":1,"stack":"web","volumes":["web_cache","web_data"]}`
	for _, e := range []struct{ name, body string }{
		{stackManifestName, manifest}, {"stack/compose.yaml", compose}, {"volumes/web_data.tar.gz", string(vol)},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(e.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	res = &VerifyResult{Passed: true}
	verifyStackArchive(res, &lossy)
	if res.Passed {
		t.Fatalf("archive missing web_cache passed: %+v", res.Checks)
	}

	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tarStackWithVolumes(ctx, fake, dir, "web", io.Discard); err == nil {
		t.Fatal("unparseable compose must fail the backup")
	}
}

func TestRunNotificationAudience(t *testing.T) {
	tests := []struct {
		name         string
		job          Job
		wantPerm     rbac.Perm
		wantHost     string
		wantUnscoped bool
	}{
		{"local volume job", Job{Sources: []Source{{Type: "volume", Name: "pg"}}}, rbac.PermBackupsView, "local", false},
		{"agent host job", Job{HostID: "h1", Sources: []Source{{Type: "stack", Name: "web"}}}, rbac.PermBackupsView, "h1", false},
		{"system job", Job{Sources: []Source{{Type: "system"}}}, rbac.PermSystemUpdate, "local", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := runNotification(&tt.job, 7)
			if in.Perm != tt.wantPerm || in.HostID != tt.wantHost || in.Unscoped != tt.wantUnscoped {
				t.Errorf("got perm %q host %q unscoped %v", in.Perm, in.HostID, in.Unscoped)
			}
			if in.Link != "/backups/runs/7" || in.UserID != "" {
				t.Errorf("link %q user %q", in.Link, in.UserID)
			}
		})
	}
}
func (f *fakeHost) VolumeImport(context.Context, string, io.Reader, bool) error {
	return errors.New("fakeHost: import not supported")
}

func (f *fakeHost) VolumeInUse(context.Context, string) ([]string, error) { return nil, nil }
