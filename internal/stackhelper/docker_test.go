//go:build integration

package stackhelper

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// dockerClient connects to the local daemon or skips the test.
func dockerClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Skip(err)
	}
	if _, err := cli.Ping(context.Background(), client.PingOptions{}); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// runAsRoot runs script in the helper image with dir mounted at /stack.
func runAsRoot(t *testing.T, cli *client.Client, dir, script string) {
	t.Helper()
	ctx := context.Background()
	h := New(cli)
	if err := h.ensureImage(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: Image, Cmd: []string{"sh", "-c", script}},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{Type: mount.TypeBind, Source: dir, Target: mountPoint}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.remove(ctx, resp.ID)
	if _, err := cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	wait := cli.ContainerWait(ctx, resp.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case s := <-wait.Result:
		if s.StatusCode != 0 {
			t.Fatalf("%q exited %d", script, s.StatusCode)
		}
	case err := <-wait.Error:
		t.Fatal(err)
	}
}

// foreignStack builds base/db with a pgdata directory owned by uid 999,
// mode 0700, the way the postgres image leaves its bind directory.
func foreignStack(t *testing.T, cli *client.Client) (base, dir string) {
	t.Helper()
	base = t.TempDir()
	dir = filepath.Join(base, "db")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAsRoot(t, cli, dir, "mkdir -p /stack/pgdata/base && echo 17 > /stack/pgdata/PG_VERSION && "+
		"touch /stack/pgdata/.hidden && chown -R 999:999 /stack/pgdata && chmod 0700 /stack/pgdata /stack/pgdata/base")
	t.Cleanup(func() {
		if _, err := os.Stat(dir); err == nil {
			runAsRoot(t, cli, dir, "rm -rf /stack/pgdata")
		}
	})
	return base, dir
}

func TestDockerRemoveAllForeignOwned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an unprivileged test user")
	}
	cli := dockerClient(t)
	base, dir := foreignStack(t, cli)
	if err := os.RemoveAll(dir); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("direct delete = %v, want a permission error", err)
	}
	if err := New(cli).RemoveAll(context.Background(), base, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dir still there: %v", err)
	}
}

func TestDockerHelperRefusesSwappedMount(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an unprivileged test user")
	}
	cli := dockerClient(t)
	base, dir := foreignStack(t, cli)
	tg, err := resolve(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	tg.id = "1:1"
	if err := New(cli).empty(context.Background(), tg); err == nil || !strings.Contains(err.Error(), "not the one that was checked") {
		t.Fatalf("empty = %v, want the swap refused", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err != nil {
		t.Fatalf("helper deleted files of a directory it should have refused: %v", err)
	}
}
