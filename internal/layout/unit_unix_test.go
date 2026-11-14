//go:build unix

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testUnit() ServerUnit {
	return ServerUnit{Binary: "/usr/local/bin/dockmesh", EnvFile: ConfigFile, DataDir: "/var/lib/dockmesh"}
}

func unitOpts(t *testing.T) (UnitWriteOptions, string) {
	t.Helper()
	dir := t.TempDir()
	unitDir := filepath.Join(dir, "system")
	if err := os.Mkdir(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return UnitWriteOptions{
		UnitFile:     filepath.Join(unitDir, "dockmesh.service"),
		PrevUnitFile: filepath.Join(dir, "dockmesh.service.prev"),
	}, unitDir
}

func TestWriteServerUnit(t *testing.T) {
	const legacy = "[Service]\nUser=dockmesh\nGroup=docker\nExecStart=/usr/local/bin/dockmesh serve\n"
	tests := []struct {
		name        string
		existing    string // "" = no unit yet
		sameAsUnit  bool   // existing unit equals the rendered one
		wantChanged bool
		wantPrev    string // "" = no prev file
	}{
		{"fresh install", "", false, true, ""},
		{"legacy unit is kept as prev", legacy, false, true, legacy},
		{"same unit is left alone", "", true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, unitDir := unitOpts(t)
			dropIn := filepath.Join(unitDir, "dockmesh.service.d", "override.conf")
			if err := os.MkdirAll(filepath.Dir(dropIn), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dropIn, []byte("[Service]\nEnvironment=FOO=1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			want, err := RenderServerUnit(testUnit())
			if err != nil {
				t.Fatal(err)
			}
			existing := tt.existing
			if tt.sameAsUnit {
				existing = string(want)
			}
			if existing != "" {
				if err := os.WriteFile(opts.UnitFile, []byte(existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			changed, err := WriteServerUnit(testUnit(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if got, _ := os.ReadFile(opts.UnitFile); string(got) != string(want) {
				t.Errorf("unit:\n%s", got)
			}
			fi, err := os.Stat(opts.UnitFile)
			if err != nil || fi.Mode().Perm() != 0o644 {
				t.Errorf("unit mode %v, err %v", fi.Mode(), err)
			}
			prev, err := os.ReadFile(opts.PrevUnitFile)
			switch {
			case tt.wantPrev == "" && err == nil:
				t.Errorf("unexpected prev file:\n%s", prev)
			case tt.wantPrev != "" && string(prev) != tt.wantPrev:
				t.Errorf("prev = %q, err %v", prev, err)
			}
			if got, _ := os.ReadFile(dropIn); string(got) != "[Service]\nEnvironment=FOO=1\n" {
				t.Errorf("drop-in changed: %q", got)
			}
		})
	}
}

func TestWriteServerUnitRefusedValueKeepsOldUnit(t *testing.T) {
	opts, _ := unitOpts(t)
	if err := os.WriteFile(opts.UnitFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	u := testUnit()
	u.WritablePaths = []string{"/srv/backups\nExecStartPre=/bin/sh"}
	if _, err := WriteServerUnit(u, opts); err == nil {
		t.Fatal("want an error")
	}
	if got, _ := os.ReadFile(opts.UnitFile); string(got) != "old" {
		t.Errorf("unit = %q", got)
	}
	if _, err := os.Lstat(opts.PrevUnitFile); !os.IsNotExist(err) {
		t.Errorf("prev file written: %v", err)
	}
}

func TestWriteServerUnitRefusesSymlink(t *testing.T) {
	opts, unitDir := unitOpts(t)
	target := filepath.Join(unitDir, "elsewhere.service")
	if err := os.WriteFile(target, []byte("linked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, opts.UnitFile); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteServerUnit(testUnit(), opts); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("want a symlink error, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "linked" {
		t.Errorf("link target changed: %q", got)
	}
}

func TestRestoreServerUnit(t *testing.T) {
	opts, _ := unitOpts(t)
	if err := RestoreServerUnit(opts); err == nil {
		t.Fatal("restore without a prev file must fail")
	}
	if err := os.WriteFile(opts.UnitFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteServerUnit(testUnit(), opts); err != nil {
		t.Fatal(err)
	}
	if err := RestoreServerUnit(opts); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(opts.UnitFile); string(got) != "old" {
		t.Errorf("unit after restore = %q", got)
	}
}

func TestReadEnvFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "dockmesh.env")
	if err := os.WriteFile(good, []byte("DOCKMESH_DATA_DIR=/data\n# c\nDOCKMESH_STACKS_ROOT=\"/srv/stacks\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	env, err := ReadEnvFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if env["DOCKMESH_DATA_DIR"] != "/data" || env["DOCKMESH_STACKS_ROOT"] != "/srv/stacks" {
		t.Errorf("env = %v", env)
	}

	link := filepath.Join(dir, "link.env")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	writable := filepath.Join(dir, "writable.env")
	if err := os.WriteFile(writable, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o666); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, writable, "relative.env"} {
		if _, err := ReadEnvFile(p); err == nil {
			t.Errorf("%s: want an error", p)
		}
	}
}

func TestWriteServerUnitDropsStalePrev(t *testing.T) {
	opts, _ := unitOpts(t)
	if err := os.WriteFile(opts.PrevUnitFile, []byte("from an earlier install"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteServerUnit(testUnit(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(opts.PrevUnitFile); !os.IsNotExist(err) {
		t.Errorf("stale prev file kept: %v", err)
	}
	if err := RestoreServerUnit(opts); err == nil {
		t.Error("restore after a fresh write must fail")
	}
}

func TestWriteServerUnitRefusesSymlinkIntoSystemTree(t *testing.T) {
	opts, _ := unitOpts(t)
	dir := t.TempDir()
	link := filepath.Join(dir, "audit")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	u := testUnit()
	u.WritablePaths = []string{link}
	if _, err := WriteServerUnit(u, opts); !errors.Is(err, ErrBadUnitValue) {
		t.Fatalf("want ErrBadUnitValue, got %v", err)
	}
	if _, err := os.Lstat(opts.UnitFile); !os.IsNotExist(err) {
		t.Errorf("unit written: %v", err)
	}
}
