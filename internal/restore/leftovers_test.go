package restore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// An applied restore leaves the replaced data and stacks behind; they are
// listed with an admin notice and deleted on request, and nothing else
// next to them is touched.
func TestLeftoversAfterApply(t *testing.T) {
	cfg, _ := restoreEnv(t)
	dataDir := filepath.Dir(cfg.DBPath)
	if err := os.WriteFile(cfg.DBPath, []byte(systemDB(t, "old-admin")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.StacksRoot, "old-stack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.StacksRoot, "old-stack", "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := []string{
		filepath.Join(dataDir, PreRestoreDir, "notes"),
		filepath.Clean(cfg.StacksRoot) + PreRestoreDir + "-mine",
	}
	for _, d := range unrelated {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	arc := archive(t,
		entry{name: "dockmesh.db", body: systemDB(t, "restored-admin")},
		entry{name: "stacks/web/compose.yaml", body: "services: {}\n"},
	)
	if _, res, err := Stage(context.Background(), arc, cfg, "test", StageOptions{}); err != nil {
		t.Fatalf("stage: %v %+v", err, res)
	}
	if applied, err := ApplyStaged(cfg); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}

	list, err := Leftovers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("leftovers %+v, want the data and the stacks copy", list)
	}
	for _, l := range list {
		if l.Bytes == 0 || l.RestoredAt.IsZero() {
			t.Errorf("leftover %+v: want a size and a time", l)
		}
	}

	database, err := dbtest.Open(filepath.Join(t.TempDir(), "notices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	n := notices.NewService(database)
	ctx := context.Background()
	if err := SyncLeftoverNotice(ctx, cfg, n); err != nil {
		t.Fatal(err)
	}
	open, err := n.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Key != LeftoverNoticeKey || !strings.Contains(open[0].Body, list[0].Path) {
		t.Fatalf("notices %+v, want one naming %s", open, list[0].Path)
	}

	removed, err := RemoveLeftovers(cfg)
	if err != nil || removed != 2 {
		t.Fatalf("remove: %d %v", removed, err)
	}
	if list, _ := Leftovers(cfg); len(list) != 0 {
		t.Fatalf("still listed: %+v", list)
	}
	for _, d := range unrelated {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed: %v", d, err)
		}
	}
	if adminName(t, cfg.DBPath) != "restored-admin" {
		t.Fatal("live database touched")
	}
	if err := SyncLeftoverNotice(ctx, cfg, n); err != nil {
		t.Fatal(err)
	}
	if open, _ := n.Open(ctx); len(open) != 0 {
		t.Fatalf("notice still open: %+v", open)
	}
}
