package stackhelper

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// fakeDocker stands in for the daemon. start plays the helper's command
// against the mounted host directory; nil means the helper does nothing.
type fakeDocker struct {
	client.ImageAPIClient
	haveImage bool
	pullErr   error
	start     func(src string) error
	created   []client.ContainerCreateOptions
	removed   int
}

func (f *fakeDocker) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	if f.haveImage {
		return client.ImageInspectResult{}, nil
	}
	return client.ImageInspectResult{}, fmt.Errorf("no such image: %w", cerrdefs.ErrNotFound)
}

func (f *fakeDocker) ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
	return nil, f.pullErr
}

func (f *fakeDocker) ContainerCreate(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.created = append(f.created, o)
	return client.ContainerCreateResult{ID: fmt.Sprintf("c%d", len(f.created))}, nil
}

func (f *fakeDocker) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, nil
}

func (f *fakeDocker) ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult {
	res := make(chan container.WaitResponse, 1)
	errs := make(chan error, 1)
	var code int64
	if f.start != nil {
		if err := f.start(f.created[len(f.created)-1].HostConfig.Mounts[0].Source); err != nil {
			code = 1
		}
	}
	res <- container.WaitResponse{StatusCode: code}
	return client.ContainerWaitResult{Result: res, Error: errs}
}

func (f *fakeDocker) ContainerKill(context.Context, string, client.ContainerKillOptions) (client.ContainerKillResult, error) {
	return client.ContainerKillResult{}, nil
}

func (f *fakeDocker) ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.removed++
	return client.ContainerRemoveResult{}, nil
}

// asRoot empties src the way the helper's rm does as root: it first gives
// the test user back access to the locked directory.
func asRoot(locked string) func(src string) error {
	return func(src string) error {
		if err := os.Chmod(locked, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(src, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
}

// lockedStack builds base/<name> with a data directory the test user
// cannot enter, like a database dir owned by uid 999 with mode 0700.
func lockedStack(t *testing.T) (base, dir, locked string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can remove any directory, so the permission error cannot be simulated")
	}
	base = t.TempDir()
	dir = filepath.Join(base, "db")
	locked = filepath.Join(dir, "pgdata")
	if err := os.MkdirAll(filepath.Join(locked, "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "PG_VERSION"), []byte("17\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	return base, dir, locked
}

func TestRemoveAllPlainNeedsNoHelper(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "web")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeDocker{haveImage: true}
	if err := New(f).RemoveAll(context.Background(), base, dir); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 0 {
		t.Fatalf("helper ran for a directory the service can delete itself")
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dir still there: %v", err)
	}
}

func TestRemoveAllForeignOwned(t *testing.T) {
	base, dir, locked := lockedStack(t)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDocker{haveImage: true, start: asRoot(locked)}
	if err := New(f).RemoveAll(context.Background(), base, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dir still there: %v", err)
	}
	if len(f.created) != 1 || f.removed != 1 {
		t.Fatalf("created %d helpers, removed %d; want 1 and 1", len(f.created), f.removed)
	}
	hc := f.created[0].HostConfig
	m := hc.Mounts[0]
	if m.Source != realDir || m.Target != mountPoint || m.ReadOnly || m.BindOptions == nil || !m.BindOptions.NonRecursive {
		t.Errorf("mount = %+v, want %s on %s read-write, non-recursive", m, realDir, mountPoint)
	}
	if env := f.created[0].Config.Env; len(env) != 1 || !strings.HasPrefix(env[0], "DOCKMESH_EXPECT=") || len(env[0]) < len("DOCKMESH_EXPECT=1:1") {
		t.Errorf("helper gets no device:inode to check: %v", env)
	}
	if hc.NetworkMode != "none" || len(hc.CapDrop) != 1 || hc.CapDrop[0] != "ALL" || !hc.ReadonlyRootfs {
		t.Errorf("helper not locked down: network=%q capdrop=%v rorootfs=%v", hc.NetworkMode, hc.CapDrop, hc.ReadonlyRootfs)
	}
	if got := strings.Join(hc.CapAdd, ","); got != "DAC_OVERRIDE,FOWNER" {
		t.Errorf("CapAdd = %s", got)
	}
	if f.created[0].Config.Image != Image {
		t.Errorf("image = %s, want the pinned %s", f.created[0].Config.Image, Image)
	}
}

func TestRemoveAllHelperThatDoesNothingFails(t *testing.T) {
	base, dir, locked := lockedStack(t)
	// Exits 0 without removing anything; only the check afterwards notices.
	f := &fakeDocker{haveImage: true, start: func(string) error { return os.Chmod(locked, 0o700) }}
	err := New(f).RemoveAll(context.Background(), base, dir)
	if err == nil {
		t.Fatal("delete reported success with the directory still there")
	}
	if !strings.Contains(err.Error(), "left behind: pgdata") {
		t.Errorf("error does not name what is left: %v", err)
	}
}

func TestRemoveAllHelperFails(t *testing.T) {
	base, dir, _ := lockedStack(t)
	f := &fakeDocker{haveImage: true, start: func(string) error { return errors.New("rm failed") }}
	err := New(f).RemoveAll(context.Background(), base, dir)
	if err == nil || !strings.Contains(err.Error(), "status 1") || !strings.Contains(err.Error(), "left behind: pgdata") {
		t.Fatalf("err = %v", err)
	}
}

func TestRemoveAllAirGapped(t *testing.T) {
	base, dir, _ := lockedStack(t)
	f := &fakeDocker{pullErr: errors.New("dial tcp: lookup registry-1.docker.io: no such host")}
	err := New(f).RemoveAll(context.Background(), base, dir)
	if err == nil || !strings.Contains(err.Error(), "pull "+Image+": ") {
		t.Fatalf("err = %v, want it to name the helper image", err)
	}
	if len(f.created) != 0 {
		t.Fatal("helper created without its image")
	}
}

func TestRemoveAllWithoutDocker(t *testing.T) {
	base, dir, _ := lockedStack(t)
	var h *Helper
	err := h.RemoveAll(context.Background(), base, dir)
	if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "need Docker") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolve(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	mk := func(p string) string {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	link := func(target, name string) string {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
		return name
	}
	stack := mk(filepath.Join(base, "web"))
	mk(filepath.Join(base, "other"))
	if err := os.WriteFile(filepath.Join(base, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	linkedBase := link(base, filepath.Join(outside, "stacks"))

	tests := []struct {
		name    string
		base    string
		dir     string
		wantErr bool
	}{
		{"stack dir", base, stack, false},
		{"base through a symlink", linkedBase, filepath.Join(linkedBase, "web"), false},
		{"base itself", base, base, true},
		{"dotdot to base", base, filepath.Join(stack, ".."), true},
		{"parent of base", base, filepath.Dir(base), true},
		{"root", base, "/", true},
		{"outside", base, outside, true},
		{"symlink to outside", base, link(outside, filepath.Join(base, "escape")), true},
		{"symlink to another stack", base, link(filepath.Join(base, "other"), filepath.Join(base, "alias")), true},
		{"symlink to parent", base, link("..", filepath.Join(base, "up")), true},
		{"regular file", base, filepath.Join(base, "file"), true},
		{"missing", base, filepath.Join(base, "missing"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolve(tt.base, tt.dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolve(%s, %s) = %q, %v; wantErr %v", tt.base, tt.dir, got, err, tt.wantErr)
			}
		})
	}
}

func TestRemoveAllRefusesEscape(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a permission error")
	}
	base := t.TempDir()
	outside := t.TempDir()
	// A stack path whose real directory lies outside the base must never
	// be mounted, even when the direct delete fails.
	locked := filepath.Join(outside, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	f := &fakeDocker{haveImage: true}
	err := New(f).RemoveAll(context.Background(), base, locked)
	if !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
	if len(f.created) != 0 {
		t.Fatal("helper created for a path outside the base")
	}
}

func TestMountBelow(t *testing.T) {
	info := `22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw
30 22 0:40 / /srv/stacks rw - ext4 /dev/sdb1 rw
31 30 0:41 / /srv/stacks/plex/media\040share rw - nfs host:/media rw
32 22 0:42 / /srv/stacks/plexa rw - tmpfs tmpfs rw
`
	tests := []struct {
		dir  string
		want string
	}{
		{"/srv/stacks/plex", "media share"},
		{"/srv/stacks/web", ""},
		{"/srv/stacks/plexa", ""},
		{"/srv/stacks/plex/media share", ""},
	}
	for _, tt := range tests {
		got, err := mountBelowIn(strings.NewReader(info), tt.dir)
		if err != nil || got != tt.want {
			t.Errorf("mountBelowIn(%q) = %q, %v; want %q", tt.dir, got, err, tt.want)
		}
	}
}
