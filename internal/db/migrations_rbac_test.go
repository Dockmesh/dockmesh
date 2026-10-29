package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func hasPerm(t *testing.T, database *sql.DB, role, perm string) bool {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM role_permissions WHERE role_name = ? AND permission = ?`, role, perm).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// The upgrade moves archive download to backups.download and takes
// system.update from host-admin; custom roles that could restore keep
// downloading, and admins are told which custom roles lost something.
func TestBackupDownloadPermissionMigration(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "059_api_token_owner.sql"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO users (id, username, password, role) VALUES ('u1', 'hadmin', 'x', 'host-admin')`,
		`INSERT INTO roles (name, display, builtin) VALUES ('restorer', 'Restorer', 0), ('watcher', 'Watcher', 0), ('shell', 'Shell', 0)`,
		`INSERT INTO role_permissions (role_name, permission) VALUES
			('restorer', 'backups.view'), ('restorer', 'backups.restore'),
			('watcher', 'backups.view'), ('watcher', 'containers.view'),
			('shell', 'containers.view'), ('shell', 'containers.exec')`,
	} {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if !hasPerm(t, database, "host-admin", "system.update") {
		t.Fatal("precondition: host-admin holds system.update before the upgrade")
	}

	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		role, perm string
		want       bool
	}{
		{"admin", "backups.download", true},
		{"host-admin", "backups.download", true},
		{"host-admin", "system.update", false},
		{"admin", "system.update", true},
		{"viewer", "backups.download", false},
		{"operator", "backups.download", false},
		{"deployer", "backups.download", false},
		{"restorer", "backups.download", true},
		{"watcher", "backups.download", false},
	} {
		if got := hasPerm(t, database, tc.role, tc.perm); got != tc.want {
			t.Errorf("%s holds %s = %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}

	var body string
	if err := database.QueryRow(`SELECT body FROM notices WHERE key = 'rbac-custom-roles-narrowed' AND audience = 'admins'`).Scan(&body); err != nil {
		t.Fatalf("custom-role notice: %v", err)
	}
	for _, want := range []string{
		"No longer download backup archives: watcher",
		"No longer read container files: watcher",
		"No longer download system backups or export the backup key: restorer",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("notice body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "shell") {
		t.Errorf("role that kept its rights is listed:\n%s", body)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM notices WHERE key = 'rbac-host-admin-no-system-settings'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("host-admin notice: n=%d err=%v", n, err)
	}
}

// A fresh install has no custom roles and no host-admin users, so the
// upgrade raises nothing.
func TestBackupDownloadPermissionMigrationQuietOnFreshInstall(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM notices`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("fresh install raised %d notices", n)
	}
}
