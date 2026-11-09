package stacks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func saveFixture(t *testing.T) (*Manager, string, *Detail) {
	t.Helper()
	root := t.TempDir()
	m, err := NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.Create("app", "a: 1\n", "K=1\n"); err != nil {
		t.Fatal(err)
	}
	d, err := m.Get("app")
	if err != nil {
		t.Fatal(err)
	}
	return m, filepath.Join(root, "app"), d
}

func TestSaveConflicts(t *testing.T) {
	tests := []struct {
		name      string
		outside   map[string]string
		in        func(d *Detail) SaveInput
		wantFiles []string
	}{
		{
			name: "unchanged",
			in: func(d *Detail) SaveInput {
				return SaveInput{Compose: "a: 2\n", Env: "K=2\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion}
			},
		},
		{
			name:    "compose changed",
			outside: map[string]string{"compose.yaml": "a: 9\n"},
			in: func(d *Detail) SaveInput {
				return SaveInput{Compose: "a: 2\n", Env: "K=1\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion}
			},
			wantFiles: []string{"compose.yaml"},
		},
		{
			name:    "env changed",
			outside: map[string]string{".env": "K=9\n"},
			in: func(d *Detail) SaveInput {
				return SaveInput{Compose: "a: 2\n", Env: "K=2\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion}
			},
			wantFiles: []string{".env"},
		},
		{
			name:    "both changed",
			outside: map[string]string{"compose.yaml": "a: 9\n", ".env": "K=9\n"},
			in: func(d *Detail) SaveInput {
				return SaveInput{Compose: "a: 2\n", Env: "K=2\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion}
			},
			wantFiles: []string{"compose.yaml", ".env"},
		},
		{
			name:    "disk already holds the saved content",
			outside: map[string]string{"compose.yaml": "a: 2\n"},
			in: func(d *Detail) SaveInput {
				return SaveInput{Compose: "a: 2\n", Env: "K=1\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion}
			},
		},
		{
			name:    "no version keeps working",
			outside: map[string]string{"compose.yaml": "a: 9\n"},
			in:      func(*Detail) SaveInput { return SaveInput{Compose: "a: 2\n", Env: "K=1\n"} },
		},
		{
			name:    "keep env ignores the env version",
			outside: map[string]string{".env": "K=9\n"},
			in: func(d *Detail) SaveInput {
				return SaveInput{Compose: "a: 2\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion, KeepEnv: true}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, dir, d := saveFixture(t)
			for file, content := range tt.outside {
				if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := m.Get("app")
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := m.Save("app", tt.in(d))
			if tt.wantFiles == nil {
				if err != nil {
					t.Fatalf("save: %v", err)
				}
				after, err := m.Get("app")
				if err != nil {
					t.Fatal(err)
				}
				if got.ComposeVersion != after.ComposeVersion {
					t.Errorf("returned compose version %s, disk has %s", got.ComposeVersion, after.ComposeVersion)
				}
				return
			}
			var conflict *ConflictError
			if !errors.As(err, &conflict) || !errors.Is(err, ErrModifiedOnDisk) {
				t.Fatalf("err = %v, want a conflict", err)
			}
			if len(conflict.Files) != len(tt.wantFiles) {
				t.Fatalf("files = %v, want %v", conflict.Files, tt.wantFiles)
			}
			for i, f := range tt.wantFiles {
				if conflict.Files[i] != f {
					t.Errorf("files = %v, want %v", conflict.Files, tt.wantFiles)
				}
			}
			if conflict.Current.Compose != before.Compose || conflict.Current.Env != before.Env {
				t.Errorf("current = %q %q, want what is on disk", conflict.Current.Compose, conflict.Current.Env)
			}
			after, err := m.Get("app")
			if err != nil {
				t.Fatal(err)
			}
			if after.Compose != before.Compose || after.Env != before.Env {
				t.Errorf("a refused save wrote: %q %q", after.Compose, after.Env)
			}
		})
	}
}

func TestSaveForceReportsOverwrittenFiles(t *testing.T) {
	m, dir, d := saveFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("a: 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, overwritten, err := m.Save("app", SaveInput{Compose: "a: 2\n", Env: "K=1\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(overwritten) != 1 || overwritten[0] != "compose.yaml" {
		t.Errorf("overwritten = %v", overwritten)
	}
	after, err := m.Get("app")
	if err != nil {
		t.Fatal(err)
	}
	if after.Compose != "a: 2\n" {
		t.Errorf("compose = %q", after.Compose)
	}
}

func TestSaveKeepEnvLeavesEnvAlone(t *testing.T) {
	m, _, d := saveFixture(t)
	got, _, err := m.Save("app", SaveInput{Compose: "a: 2\n", ComposeVersion: d.ComposeVersion, KeepEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Env != "" || got.EnvVersion != "" {
		t.Errorf("returned env %q version %q", got.Env, got.EnvVersion)
	}
	after, err := m.Get("app")
	if err != nil {
		t.Fatal(err)
	}
	if after.Env != "K=1\n" {
		t.Errorf("env = %q, want it unchanged", after.Env)
	}
}

func TestSaveOfMissingStack(t *testing.T) {
	m, _, d := saveFixture(t)
	_, _, err := m.Save("gone", SaveInput{Compose: "a: 2\n", ComposeVersion: d.ComposeVersion})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSaveAfterSaveUsesReturnedVersions(t *testing.T) {
	m, _, d := saveFixture(t)
	first, _, err := m.Save("app", SaveInput{Compose: "a: 2\n", Env: "K=2\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Save("app", SaveInput{Compose: "a: 3\n", Env: "", ComposeVersion: first.ComposeVersion, EnvVersion: first.EnvVersion}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	after, err := m.Get("app")
	if err != nil {
		t.Fatal(err)
	}
	if after.Env != "" || after.EnvVersion != contentVersion("") {
		t.Errorf("env = %q, version %s", after.Env, after.EnvVersion)
	}
}

func TestSaveForceReportsUnreadableEnv(t *testing.T) {
	m, dir, d := saveFixture(t)
	if err := os.WriteFile(filepath.Join(dir, ".env.age"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := SaveInput{Compose: "a: 2\n", Env: "K=2\n", ComposeVersion: d.ComposeVersion, EnvVersion: d.EnvVersion}
	if _, _, err := m.Save("app", in); err == nil || errors.Is(err, ErrModifiedOnDisk) {
		t.Fatalf("err = %v, want the read error", err)
	}
	in.Force = true
	_, overwritten, err := m.Save("app", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(overwritten) != 1 || overwritten[0] != ".env" {
		t.Errorf("overwritten = %v", overwritten)
	}
}
