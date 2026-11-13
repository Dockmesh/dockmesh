//go:build unix

package layout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const oldEnv = `# Written by install.sh
DOCKMESH_HTTP_ADDR=:8080
DOCKMESH_DB_PATH=%DIR%/data/dockmesh.db
DOCKMESH_STACKS_ROOT=%DIR%/stacks
`

type fixture struct {
	etc, data, src string
	opts           MigrateOptions
}

func newFixture(t *testing.T, body string) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{etc: filepath.Join(base, "etc", "dockmesh"), data: filepath.Join(base, "data")}
	if err := os.MkdirAll(filepath.Dir(f.etc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.data, 0o750); err != nil {
		t.Fatal(err)
	}
	f.src = filepath.Join(f.data, "dockmesh.env")
	if body != "" {
		if err := os.WriteFile(f.src, []byte(strings.ReplaceAll(body, "%DIR%", f.data)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.opts = MigrateOptions{ConfigDir: f.etc, Candidates: []string{"", f.src}, UID: os.Getuid(), GID: os.Getgid()}
	return f
}

func TestMigrateMovesAndLinks(t *testing.T) {
	f := newFixture(t, oldEnv)
	before, _ := os.ReadFile(f.src)

	res, err := Migrate(f.opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != Moved || res.From != f.src || res.DataDir != f.data {
		t.Fatalf("result = %+v", res)
	}
	target := filepath.Join(f.etc, "dockmesh.env")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), string(before)) {
		t.Errorf("moved file does not start with the old content:\n%s", got)
	}
	if env := parseEnv(got); env["DOCKMESH_DATA_DIR"] != f.data || env["DOCKMESH_DB_PATH"] != f.data+"/data/dockmesh.db" {
		t.Errorf("moved env = %v", env)
	}
	if link, err := os.Readlink(f.src); err != nil || link != target {
		t.Errorf("old place: link %q, err %v; want a symlink to %s", link, err, target)
	}
	checks := []struct {
		path string
		mode os.FileMode
	}{{f.etc, 0o750 | os.ModeDir}, {target, 0o600}}
	for _, c := range checks {
		fi, err := os.Stat(c.path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != c.mode {
			t.Errorf("%s mode = %v, want %v", c.path, fi.Mode(), c.mode)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(f.etc, ".*"))
	more, _ := filepath.Glob(filepath.Join(f.data, ".*"))
	if len(leftovers)+len(more) != 0 {
		t.Errorf("temp files left: %v %v", leftovers, more)
	}

	again, err := Migrate(f.opts)
	if err != nil || again.Outcome != AlreadyMigrated {
		t.Fatalf("second run = %+v, %v; want AlreadyMigrated", again, err)
	}
	if after, _ := os.ReadFile(target); string(after) != string(got) {
		t.Error("second run changed the config")
	}
}

func TestMigrateOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		setup   func(t *testing.T, f *fixture)
		want    Outcome
		wantErr error
		dataDir string
	}{
		{name: "nothing to move", want: NothingFound},
		{name: "setup not finished", body: oldEnv + "DOCKMESH_SETUP_FORCE=true\n", want: SetupPending},
		{name: "setup flag false still waits", body: oldEnv + "DOCKMESH_SETUP_FORCE=false\n", want: SetupPending},
		{name: "data dir already set", body: "DOCKMESH_DATA_DIR=/srv/dm\n", want: Moved, dataDir: "/srv/dm"},
		{name: "no db path uses the file's dir", body: "DOCKMESH_HTTP_ADDR=:8080", want: Moved, dataDir: "%DIR%"},
		{name: "db path outside data/", body: "DOCKMESH_DB_PATH=/opt/dm/dockmesh.db\n", want: Moved, dataDir: "/opt/dm"},
		{
			name: "group-writable source refused", body: oldEnv, wantErr: ErrUntrusted,
			setup: func(t *testing.T, f *fixture) { chmod(t, f.src, 0o620) },
		},
		{
			name: "root file readable by another group refused", body: oldEnv, wantErr: ErrUntrusted,
			setup: func(t *testing.T, f *fixture) {
				chmod(t, f.src, 0o640)
				f.opts.GID = os.Getgid() + 1
			},
		},
		{
			name: "group-writable dir refused", body: oldEnv, wantErr: ErrUntrusted,
			setup: func(t *testing.T, f *fixture) { chmod(t, f.data, 0o770) },
		},
		{
			name: "hard-linked source refused", body: oldEnv, wantErr: ErrUntrusted,
			setup: func(t *testing.T, f *fixture) {
				if err := os.Link(f.src, filepath.Join(t.TempDir(), "other")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked source skipped", want: NothingFound,
			setup: func(t *testing.T, f *fixture) {
				target := filepath.Join(t.TempDir(), "dockmesh.env")
				if err := os.WriteFile(target, []byte("DOCKMESH_HTTP_ADDR=:1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, f.src); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "target is a symlink", body: oldEnv, wantErr: errAny,
			setup: func(t *testing.T, f *fixture) {
				if err := os.MkdirAll(f.etc, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/nonexistent", filepath.Join(f.etc, "dockmesh.env")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "config dir is a file", body: oldEnv, wantErr: errAny,
			setup: func(t *testing.T, f *fixture) {
				if err := os.WriteFile(f.etc, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, tt.body)
			if tt.setup != nil {
				tt.setup(t, &f)
			}
			before, _ := os.Lstat(f.src)
			beforeData, _ := os.ReadFile(f.src)
			res, err := Migrate(f.opts)
			if tt.wantErr != nil {
				if err == nil || (tt.wantErr != errAny && !errors.Is(err, tt.wantErr)) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				after, _ := os.Lstat(f.src)
				afterData, _ := os.ReadFile(f.src)
				if before != nil && (after == nil || after.Mode() != before.Mode() || string(afterData) != string(beforeData)) {
					t.Error("a failed run changed the old env file")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tt.want {
				t.Fatalf("outcome = %v, want %v", res.Outcome, tt.want)
			}
			if want := strings.ReplaceAll(tt.dataDir, "%DIR%", f.data); res.DataDir != want {
				t.Errorf("data dir = %q, want %q", res.DataDir, want)
			}
			if tt.want != Moved {
				if _, err := os.Lstat(filepath.Join(f.etc, "dockmesh.env")); err == nil {
					t.Error("config written although nothing was moved")
				}
			}
		})
	}
}

var errAny = errors.New("any error")

func chmod(t *testing.T, p string, m os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, m); err != nil {
		t.Fatal(err)
	}
}

func TestParseEnv(t *testing.T) {
	got := parseEnv([]byte("# c\nA=1\nexport B = \"two\"\nA=3\nnoeq\n=x\nC='q'\n"))
	want := map[string]string{"A": "3", "B": "two", "C": "q"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestMigrateKeepsOwnerOnlyReadable(t *testing.T) {
	tests := []struct {
		name    string
		srcMode os.FileMode
		want    os.FileMode
	}{
		{"owner only stays owner only", 0o600, 0o600},
		{"group-readable becomes 0640", 0o640, 0o640},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, oldEnv)
			chmod(t, f.src, tt.srcMode)
			if _, err := Migrate(f.opts); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(filepath.Join(f.etc, "dockmesh.env"))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != tt.want {
				t.Errorf("mode = %v, want %v", fi.Mode().Perm(), tt.want)
			}
		})
	}
}
