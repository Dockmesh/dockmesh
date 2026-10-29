package restore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

// systemDB returns the bytes of a migrated database with one admin
// named user.
func systemDB(t *testing.T, user string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.db")
	database, err := db.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users (id, username, password, role) VALUES (?, ?, 'x', 'admin')`, user, user); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func adminName(t *testing.T, path string) string {
	t.Helper()
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var name string
	if err := database.QueryRow(`SELECT username FROM users WHERE role = 'admin'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestStageAndApply(t *testing.T) {
	cfg, _ := restoreEnv(t)
	dataDir := filepath.Dir(cfg.DBPath)
	if err := os.WriteFile(cfg.DBPath, []byte(systemDB(t, "old-admin")), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"secrets.age-key": "old-key", "jwt.secret": "old-jwt"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(cfg.StacksRoot, "old-stack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}

	arc := archive(t,
		entry{name: "dockmesh.db", body: systemDB(t, "restored-admin")},
		entry{name: "stacks/web/compose.yaml", body: "services: {}\n"},
		entry{name: "data/secrets.age-key", body: "new-key"},
	)
	if _, res, err := Stage(context.Background(), arc, cfg, "test"); err != nil {
		t.Fatalf("stage: %v %+v", err, res)
	}
	// Staging leaves the live install alone.
	if adminName(t, cfg.DBPath) != "old-admin" {
		t.Fatal("live database changed by staging")
	}
	if info, _ := Staged(cfg); info == nil || info.Source != "test" {
		t.Fatalf("staged info: %+v", info)
	}

	applied, err := ApplyStaged(cfg)
	if err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	if adminName(t, cfg.DBPath) != "restored-admin" {
		t.Fatal("database not swapped")
	}
	if b, _ := os.ReadFile(filepath.Join(dataDir, "secrets.age-key")); string(b) != "new-key" {
		t.Fatalf("key not swapped: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dataDir, "jwt.secret")); string(b) != "old-jwt" {
		t.Fatal("files the backup doesn't have must stay")
	}
	if _, err := os.Stat(filepath.Join(cfg.StacksRoot, "web", "compose.yaml")); err != nil {
		t.Fatalf("stacks not swapped: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "backups")); err != nil {
		t.Fatal("local backups folder moved away")
	}
	if info, _ := Staged(cfg); info != nil {
		t.Fatal("staging area left behind")
	}
	// The replaced files are kept.
	prev, _ := filepath.Glob(filepath.Join(dataDir, PreRestoreDir, "*", "secrets.age-key"))
	if len(prev) != 1 {
		t.Fatalf("old key not kept: %v", prev)
	}
	if again, err := ApplyStaged(cfg); err != nil || again {
		t.Fatalf("second apply: %v %v", again, err)
	}
}

func TestApplyResumesAfterCrash(t *testing.T) {
	cfg, _ := restoreEnv(t)
	dataDir := filepath.Dir(cfg.DBPath)
	if err := os.WriteFile(cfg.DBPath, []byte(systemDB(t, "old-admin")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "secrets.age-key"), []byte("old-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	arc := archive(t,
		entry{name: "dockmesh.db", body: systemDB(t, "restored-admin")},
		entry{name: "data/secrets.age-key", body: "new-key"},
	)
	if _, _, err := Stage(context.Background(), arc, cfg, "test"); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the database was swapped: run the first
	// steps by hand with the marker the real apply writes.
	p := pathsFor(cfg)
	info, _ := readMarker(p)
	info.PrevDir = filepath.Join(dataDir, PreRestoreDir, "crash")
	info.PrevStacks = cfg.StacksRoot + PreRestoreDir + "-crash"
	if err := writeMarker(p, info); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(info.PrevDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cfg.DBPath, filepath.Join(info.PrevDir, "dockmesh.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(p.data, "dockmesh.db"), cfg.DBPath); err != nil {
		t.Fatal(err)
	}

	if applied, err := ApplyStaged(cfg); err != nil || !applied {
		t.Fatalf("resume: %v %v", applied, err)
	}
	if adminName(t, cfg.DBPath) != "restored-admin" {
		t.Fatal("database lost on resume")
	}
	if b, _ := os.ReadFile(filepath.Join(dataDir, "secrets.age-key")); string(b) != "new-key" {
		t.Fatalf("key not swapped on resume: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(info.PrevDir, "dockmesh.db")); len(b) == 0 {
		t.Fatal("old database not kept")
	}
}

func TestStageRejectsBrokenBackup(t *testing.T) {
	cfg, _ := restoreEnv(t)
	arc := archive(t, entry{name: "stacks/web/compose.yaml", body: "x"})
	if _, _, err := Stage(context.Background(), arc, cfg, "test"); err == nil {
		t.Fatal("archive without a database staged")
	}
	if info, _ := Staged(cfg); info != nil {
		t.Fatal("failed staging left a marker")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.DBPath), StagingDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed staging left files")
	}
}
