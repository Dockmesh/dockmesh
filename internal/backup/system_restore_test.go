package backup

import (
	"bytes"
	"context"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/restore"
)

// systemSource is a server's data and stacks directories with the modes
// a real install has: a private git cache, a 0750 stacks tree and a 0700
// bind directory.
func systemSource(t *testing.T) SystemPaths {
	t.Helper()
	base := t.TempDir()
	paths := SystemPaths{DataDir: filepath.Join(base, "data"), StacksRoot: filepath.Join(base, "stacks")}
	paths.DBPath = filepath.Join(paths.DataDir, "dockmesh.db")
	for _, d := range []struct {
		rel  string
		mode fs.FileMode
	}{
		{"data", 0o700}, {"data/git-cache", 0o700}, {"data/git-cache/repo", 0o750},
		{"stacks", 0o750}, {"stacks/app", 0o750}, {"stacks/app/bind", 0o700},
	} {
		mkdirMode(t, filepath.Join(base, d.rel), d.mode)
	}
	for _, f := range []struct {
		rel  string
		mode fs.FileMode
	}{
		{"data/secrets.age-key", 0o400}, {"data/git-cache/repo/HEAD", 0o640},
		{"stacks/app/compose.yaml", 0o640}, {"stacks/app/bind/marker", 0o600},
	} {
		p := filepath.Join(base, f.rel)
		if err := os.WriteFile(p, []byte("x"), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	database, err := dbtest.Open(paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('a', 'admin', 'x');
		INSERT INTO role_assignments (user_id, role_name, scope_type) VALUES ('a', 'admin', 'global')`); err != nil {
		t.Fatal(err)
	}
	return paths
}

func mkdirMode(t *testing.T, p string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(p, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// restoreTarget is an empty install as setup leaves it.
func restoreTarget(t *testing.T) *config.Config {
	t.Helper()
	base := t.TempDir()
	cfg := &config.Config{DBPath: filepath.Join(base, "data", "dockmesh.db"), StacksRoot: filepath.Join(base, "stacks")}
	mkdirMode(t, filepath.Dir(cfg.DBPath), 0o700)
	mkdirMode(t, cfg.StacksRoot, 0o750)
	return cfg
}

func stageAndApply(t *testing.T, cfg *config.Config, archive []byte, files restore.FileHelper) *restore.Counts {
	t.Helper()
	counts, _, err := restore.Stage(context.Background(), bytes.NewReader(archive), cfg, "test", restore.StageOptions{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restore.ApplyStaged(cfg); err != nil {
		t.Fatal(err)
	}
	return counts
}

func wantModes(t *testing.T, cfg *config.Config, want map[string]fs.FileMode) {
	t.Helper()
	for rel, mode := range want {
		p := filepath.Join(filepath.Dir(cfg.DBPath), rel)
		if after, ok := strings.CutPrefix(rel, "stacks"); ok {
			p = cfg.StacksRoot + after
		}
		fi, err := os.Lstat(p)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if got := fi.Mode().Perm(); got != mode {
			t.Errorf("%s: mode %#o, want %#o", rel, got, mode)
		}
	}
}

var restoredModes = map[string]fs.FileMode{
	"git-cache":               0o700,
	"git-cache/repo":          0o750,
	"git-cache/repo/HEAD":     0o640,
	"secrets.age-key":         0o400,
	"stacks":                  0o750,
	"stacks/app":              0o750,
	"stacks/app/bind":         0o700,
	"stacks/app/bind/marker":  0o600,
	"stacks/app/compose.yaml": 0o640,
}

func TestSystemRestoreKeepsModes(t *testing.T) {
	src := systemSource(t)
	database, err := dbtest.Open(src.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var buf bytes.Buffer
	if _, err := tarSystem(context.Background(), database, src, nil, &buf); err != nil {
		t.Fatal(err)
	}
	cfg := restoreTarget(t)
	stageAndApply(t, cfg, buf.Bytes(), nil)
	wantModes(t, cfg, restoredModes)
}

// The fixture was written by the system backup of v0.5.4 (owner names
// replaced); released archives carry every mode and owner in the headers.
func TestSystemRestoreOfV054Archive(t *testing.T) {
	archive, err := os.ReadFile("testdata/system-v0.5.4.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	cfg := restoreTarget(t)
	stageAndApply(t, cfg, archive, nil)
	want := maps.Clone(restoredModes)
	// The fixture's source created git-cache/repo as 0700.
	want["git-cache/repo"] = 0o700
	wantModes(t, cfg, want)
}
