//go:build integration

package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/stackhelper"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func rootRun(t *testing.T, cli *client.Client, dir, script string) {
	t.Helper()
	ctx := context.Background()
	resp, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: stackhelper.Image, Cmd: []string{"sh", "-c", script}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeBind, Source: dir, Target: "/stack"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = cli.ContainerRemove(ctx, resp.ID, client.ContainerRemoveOptions{Force: true}) }()
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

// TestDockerStackBackupRestoreForeignOwned backs up a stack whose pgdata
// directory belongs to uid 999 with mode 0700, restores it as another
// stack and checks owner, mode, time and content survived.
func TestDockerStackBackupRestoreForeignOwned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an unprivileged test user")
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Skip(err)
	}
	if _, err := cli.Ping(context.Background(), client.PingOptions{}); err != nil {
		t.Skip(err)
	}
	if _, err := cli.ImageInspect(context.Background(), stackhelper.Image); err != nil {
		if err := docker.PullAnonymous(context.Background(), cli, stackhelper.Image); err != nil {
			t.Skip(err)
		}
	}
	root := t.TempDir()
	m, err := stacks.NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if _, err := m.Create("db", "services: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "db")
	t.Cleanup(func() {
		for _, d := range []string{"db", "db2"} {
			if _, err := os.Stat(filepath.Join(root, d)); err == nil {
				rootRun(t, cli, filepath.Join(root, d), "rm -rf /stack/pgdata")
			}
		}
	})

	rootRun(t, cli, src, "mkdir -p /stack/pgdata/base && echo 17 > /stack/pgdata/PG_VERSION && "+
		"chown -R 999:999 /stack/pgdata && chmod 0700 /stack/pgdata && chmod 0600 /stack/pgdata/PG_VERSION && "+
		"touch -d '2020-01-02 03:04:05' /stack/pgdata/PG_VERSION /stack/pgdata")
	var archive bytes.Buffer
	if _, err := tarStackWithVolumes(context.Background(), nil, stackhelper.New(cli), src, "db", &archive); err != nil {
		t.Fatal(err)
	}
	s := &Service{stacks: m, docker: docker.Wrap(cli)}
	report := &RestoreReport{}
	if err := s.restoreStackInto(context.Background(), "db2", &archive, nil, nil, nil, false, report); err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("warnings: %v", report.Warnings)
	}
	fi, err := os.Lstat(filepath.Join(root, "db2", "pgdata"))
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 999 || st.Gid != 999 || fi.Mode().Perm() != 0o700 {
		t.Errorf("pgdata restored as %d:%d %o, want 999:999 700", st.Uid, st.Gid, fi.Mode().Perm())
	}
	if fi.ModTime().UTC().Format("2006-01-02T15:04:05") != "2020-01-02T03:04:05" {
		t.Errorf("pgdata mtime = %v", fi.ModTime().UTC())
	}
	rootRun(t, cli, filepath.Join(root, "db2"), `[ "$(cat /stack/pgdata/PG_VERSION)" = 17 ] && `+
		`[ "$(stat -c %u:%a /stack/pgdata/PG_VERSION)" = 999:600 ] && [ -d /stack/pgdata/base ]`)
	if b, err := os.ReadFile(filepath.Join(root, "db2", "compose.yaml")); err != nil || string(b) != "services: {}\n" {
		t.Errorf("compose.yaml = %q, %v", b, err)
	}
}
