package setup

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

func newStateDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

func TestNewFromDBUsesPermissions(t *testing.T) {
	adminEquivalent := []string{"system.update", "users.view", "users.create", "users.update", "users.delete"}
	tests := []struct {
		name       string
		role       string
		rolePerms  []string // custom role created with these permissions
		wantActive bool
	}{
		{name: "no users", wantActive: true},
		{name: "built-in admin", role: "admin", wantActive: false},
		{name: "only viewer", role: "viewer", wantActive: true},
		{name: "host-admin", role: "host-admin", wantActive: true},
		{name: "custom admin-equivalent role", role: "superops", rolePerms: adminEquivalent, wantActive: false},
		{name: "custom role without user management", role: "settings-only", rolePerms: []string{"system.update"}, wantActive: true},
		{name: "custom role named like admin", role: "Admin2", rolePerms: []string{"users.view"}, wantActive: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database := newStateDB(t)
			if tt.rolePerms != nil {
				if _, err := database.Exec(`INSERT INTO roles (name, display) VALUES (?, ?)`, tt.role, tt.role); err != nil {
					t.Fatalf("role: %v", err)
				}
				for _, p := range tt.rolePerms {
					if _, err := database.Exec(`INSERT INTO role_permissions (role_name, permission) VALUES (?, ?)`, tt.role, p); err != nil {
						t.Fatalf("perm: %v", err)
					}
				}
			}
			if tt.role != "" {
				if _, err := database.Exec(`INSERT INTO users (id, username, password, role) VALUES ('u1', 'u1', 'x', ?)`, tt.role); err != nil {
					t.Fatalf("user: %v", err)
				}
			}
			st, err := NewFromDB(context.Background(), database)
			if err != nil {
				t.Fatalf("NewFromDB: %v", err)
			}
			if st.Active() != tt.wantActive {
				t.Fatalf("Active() = %v, want %v", st.Active(), tt.wantActive)
			}
		})
	}
}

func TestCommitLock(t *testing.T) {
	st := NewForced()
	if !st.TryBeginCommit() {
		t.Fatal("first commit refused")
	}
	if st.TryBeginCommit() {
		t.Fatal("second concurrent commit accepted")
	}
	st.EndCommit(false)
	if !st.Active() {
		t.Fatal("failed install ended setup mode")
	}
	if !st.TryBeginCommit() {
		t.Fatal("retry after a failed install refused")
	}
	st.EndCommit(true)
	if st.Active() {
		t.Fatal("successful install left setup mode active")
	}
	if st.TryBeginCommit() {
		t.Fatal("commit accepted after setup finished")
	}
}

func TestAddsToDockerGroup(t *testing.T) {
	tests := []struct {
		mode      string
		addDocker bool
		want      bool
	}{
		{"create", true, true},
		{"create", false, false},
		{"existing", true, false},
		{"existing", false, false},
	}
	for _, tt := range tests {
		var in CommitInput
		in.ServiceUser.Mode = tt.mode
		in.ServiceUser.AddDocker = tt.addDocker
		if got := in.AddsToDockerGroup(); got != tt.want {
			t.Errorf("mode=%s add=%v: got %v, want %v", tt.mode, tt.addDocker, got, tt.want)
		}
	}
}
