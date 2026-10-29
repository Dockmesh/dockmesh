package stacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

func newSumManager(t *testing.T) *Manager {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	m.SetSumStore(NewSumStore(database))
	return m
}

func TestModifiedOutside(t *testing.T) {
	m := newSumManager(t)
	if _, err := m.Create("web", "services: {}\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.Dir("web")

	changed, err := m.ModifiedOutside("web")
	if err != nil || len(changed) != 0 {
		t.Fatalf("fresh stack: %v, %v", changed, err)
	}
	if _, err := m.Update("web", "services: {a: {image: x}}\n", ""); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("web"); len(changed) != 0 {
		t.Fatalf("own update reported as outside change: %v", changed)
	}

	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {evil: {image: x}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("B=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err = m.ModifiedOutside("web")
	if err != nil || !reflect.DeepEqual(changed, []string{".env", "compose.yaml"}) {
		t.Fatalf("outside edit: %v, %v", changed, err)
	}

	if err := m.AcceptExternal("web"); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("web"); len(changed) != 0 {
		t.Errorf("after accept: %v", changed)
	}
}

func TestModifiedOutsideRecordsUntrackedStack(t *testing.T) {
	m := newSumManager(t)
	dir := filepath.Join(m.rootAbs, "legacy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.ModifiedOutside("legacy"); err != nil || len(changed) != 0 {
		t.Fatalf("untracked stack: %v, %v", changed, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {x: {}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, _ := m.ModifiedOutside("legacy"); len(changed) != 1 {
		t.Errorf("edit after baseline not detected: %v", changed)
	}
}

func openNotices(t *testing.T, n *notices.Service, key string) []notices.Notice {
	t.Helper()
	open, err := n.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []notices.Notice
	for _, o := range open {
		if o.Key == key {
			out = append(out, o)
		}
	}
	return out
}

func TestGitDeployHoldNotice(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		release func(m *Manager) error
	}{
		{"deploy", func(m *Manager) error { m.Deployed(ctx, "web"); return nil }},
		{"accept", func(m *Manager) error { return m.AcceptExternal("web") }},
		{"delete", func(m *Manager) error { return m.Delete("web") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newSumManager(t)
			n := notices.NewService(m.sums.db)
			m.SetNotices(n)
			key := GitDeployBlockedNoticeKey("web")
			if _, err := m.Create("web", "services: {}\n", "A=1\n"); err != nil {
				t.Fatal(err)
			}
			if err := m.CheckGitDeploy(ctx, "web"); err != nil {
				t.Fatalf("unchanged stack: %v", err)
			}
			if got := openNotices(t, n, key); len(got) != 0 {
				t.Fatalf("notice for unchanged stack: %v", got)
			}

			dir, _ := m.Dir("web")
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("B=2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if err := m.CheckGitDeploy(ctx, "web"); !errors.Is(err, ErrModifiedOutside) {
					t.Fatalf("poll %d: err = %v, want ErrModifiedOutside", i, err)
				}
			}
			got := openNotices(t, n, key)
			if len(got) != 1 {
				t.Fatalf("open notices = %d, want 1", len(got))
			}
			if !strings.Contains(got[0].Body, ".env") || got[0].Link != "/stacks/web" {
				t.Errorf("notice = %+v", got[0])
			}

			deployer := notices.Viewer{UserID: "u1", Allowed: func(p string) bool { return p == string(rbac.PermStacksDeploy) }}
			viewer := notices.Viewer{UserID: "u2", Allowed: func(p string) bool { return p == string(rbac.PermStacksView) }}
			if v, _ := n.ForViewer(ctx, deployer); len(v) != 1 {
				t.Errorf("deployer sees %d notices, want 1", len(v))
			}
			if v, _ := n.ForViewer(ctx, viewer); len(v) != 0 {
				t.Errorf("viewer without deploy permission sees %d notices", len(v))
			}

			if err := tt.release(m); err != nil {
				t.Fatal(err)
			}
			if got := openNotices(t, n, key); len(got) != 0 {
				t.Errorf("notice still open after %s", tt.name)
			}
		})
	}
}
