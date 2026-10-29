package host

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/mount"
)

func TestHelperTarget(t *testing.T) {
	tests := []struct {
		sub     string
		want    string
		wantErr error
	}{
		{sub: "", want: "/mnt/target"},
		{sub: "/", want: "/mnt/target"},
		{sub: "conf/app.yml", want: "/mnt/target/conf/app.yml"},
		{sub: "/conf//x/../app.yml", want: "/mnt/target/conf/app.yml"},
		{sub: "a..b", want: "/mnt/target/a..b"},
		{sub: "..", wantErr: ErrVolumePathEscape},
		{sub: "/../..", wantErr: ErrVolumePathEscape},
		{sub: "conf/../../etc/shadow", wantErr: ErrVolumePathEscape},
		{sub: "../target-other", wantErr: ErrVolumePathEscape},
		{sub: strings.Repeat("a", MaxBrowsePathLen+1), wantErr: ErrVolumePathTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			got, err := helperTarget(tt.sub)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("target = %q, want %q", got, tt.want)
			}
		})
	}
}

type fakeRunner struct {
	res     helperResult
	scripts []string
}

func (f *fakeRunner) run(_ context.Context, script string) (helperResult, error) {
	f.scripts = append(f.scripts, script)
	return f.res, nil
}

func TestHelperErrorsMapToSentinels(t *testing.T) {
	tests := []struct {
		name     string
		sub      string
		exitCode int64
		wantErr  error
		wantRun  bool
	}{
		{name: "traversal never starts a container", sub: "../../etc/passwd", wantErr: ErrVolumePathEscape},
		{name: "symlink out of volume", sub: "hostlink", exitCode: helperExitEscape, wantErr: ErrVolumePathEscape, wantRun: true},
		{name: "missing", sub: "nope", exitCode: helperExitNotFound, wantErr: fs.ErrNotExist, wantRun: true},
		{name: "wrong type", sub: "x", exitCode: helperExitNotDir, wantErr: ErrVolumeNotDir, wantRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRunner{res: helperResult{exitCode: tt.exitCode}}
			_, err := browseDirViaHelper(context.Background(), f.run, tt.sub)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("browse err = %v, want %v", err, tt.wantErr)
			}
			if ran := len(f.scripts) > 0; ran != tt.wantRun {
				t.Fatalf("helper ran = %v, want %v", ran, tt.wantRun)
			}

			code := tt.exitCode
			want := tt.wantErr
			if code == helperExitNotDir {
				code, want = helperExitNotFile, ErrVolumeNotFile
			}
			f = &fakeRunner{res: helperResult{exitCode: code}}
			if _, err := readFileViaHelper(context.Background(), f.run, tt.sub, 0); !errors.Is(err, want) {
				t.Fatalf("read err = %v, want %v", err, want)
			}
		})
	}

	f := &fakeRunner{res: helperResult{exitCode: 1, stderr: "boom"}}
	if _, err := browseDirViaHelper(context.Background(), f.run, ""); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unknown exit: err = %v, want stderr in message", err)
	}
}

func TestHelperScripts(t *testing.T) {
	f := &fakeRunner{res: helperResult{stdout: "d|4096|755|1700000000||it's\nl|0|777|1700000000|/etc|hostlink\ngarbage\n"}}
	entries, err := browseDirViaHelper(context.Background(), f.run, "/it's here")
	if err != nil {
		t.Fatal(err)
	}
	script := f.scripts[0]
	for _, want := range []string{
		`t='/mnt/target/it'\''s here'`,
		`r=$(realpath "$t")`,
		`case "$r" in /mnt/target|/mnt/target/*) ;; *) exit 70 ;; esac`,
		`[ -d "$r" ] || exit 72`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("browse script lacks %q:\n%s", want, script)
		}
	}
	if len(entries) != 2 || entries[0].Type != "dir" || entries[1].Type != "symlink" || entries[1].LinkDest != "/etc" {
		t.Errorf("entries = %+v", entries)
	}

	f = &fakeRunner{res: helperResult{stdout: "5000\nab\x00cdef"}}
	res, err := readFileViaHelper(context.Background(), f.run, "data.bin", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.scripts[0], `[ -f "$r" ] || exit 73`) || !strings.Contains(f.scripts[0], `head -c 4 "$r"`) {
		t.Errorf("read script:\n%s", f.scripts[0])
	}
	if string(res.Content) != "ab\x00c" || res.Size != 5000 || !res.Truncated || !res.Binary {
		t.Errorf("read result = %+v", res)
	}
}

func TestHelperContainerConfig(t *testing.T) {
	cfg, hostCfg := helperContainerConfig("pgdata", "true")
	if cfg.Image != helperImage || len(cfg.Cmd) != 3 || cfg.Cmd[2] != "true" {
		t.Errorf("config = %+v", cfg)
	}
	if !hostCfg.ReadonlyRootfs || hostCfg.NetworkMode != "none" || len(hostCfg.Binds) != 0 {
		t.Errorf("host config = %+v", hostCfg)
	}
	if len(hostCfg.Mounts) != 1 {
		t.Fatalf("mounts = %+v", hostCfg.Mounts)
	}
	m := hostCfg.Mounts[0]
	if m.Type != mount.TypeVolume || m.Source != "pgdata" || m.Target != helperMount || !m.ReadOnly {
		t.Errorf("mount = %+v", m)
	}
}

// Containment failures are decided before the docker client is touched,
// so a nil client must never be reached here.
func TestOnHostContainment(t *testing.T) {
	mount := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "shadow"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	haveLinks := os.Symlink(outside, filepath.Join(mount, "hostdir")) == nil

	tests := []struct {
		sub      string
		needLink bool
	}{
		{sub: "../../etc/passwd"},
		{sub: "/.."},
		{sub: "hostdir", needLink: true},
		{sub: "hostdir/shadow", needLink: true},
	}
	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			if tt.needLink && !haveLinks {
				t.Skip("symlinks not available")
			}
			_, berr := BrowseVolumeOnHost(context.Background(), nil, "v", mount, tt.sub)
			_, rerr := ReadVolumeFileOnHost(context.Background(), nil, "v", mount, tt.sub, 0)
			for _, err := range []error{berr, rerr} {
				if !errors.Is(err, ErrVolumePathEscape) {
					t.Errorf("err = %v, want ErrVolumePathEscape", err)
				}
			}
		})
	}
}

func TestAgentBrowseError(t *testing.T) {
	tests := []struct {
		msg  string
		want error
	}{
		{msg: ErrVolumePathEscape.Error(), want: ErrVolumePathEscape},
		{msg: ErrVolumePathTooLong.Error(), want: ErrVolumePathTooLong},
		{msg: ErrVolumeNotDir.Error(), want: ErrVolumeNotDir},
		{msg: ErrVolumeNotFile.Error(), want: ErrVolumeNotFile},
		{msg: ErrVolumeMountpointMissing.Error(), want: ErrVolumeMountpointMissing},
		{msg: "nope: file does not exist", want: fs.ErrNotExist},
		{msg: "openat nope: no such file or directory", want: fs.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			orig := fmt.Errorf("agent: %s", tt.msg)
			got := agentBrowseError(orig)
			if !errors.Is(got, tt.want) {
				t.Fatalf("errors.Is(%v, %v) = false", got, tt.want)
			}
			if got.Error() != orig.Error() {
				t.Fatalf("message changed: %q", got.Error())
			}
		})
	}
	other := errors.New("agent: permission denied")
	if got := agentBrowseError(other); got != other {
		t.Fatalf("unrelated error rewritten: %v", got)
	}
}
