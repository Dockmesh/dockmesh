package setup

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/notices"
)

func newStateDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestNewForBootNeverSetUpOnlyWithoutUsers(t *testing.T) {
	adminEquivalent := map[string]string{"settings": "manage", "users": "manage"}
	tests := []struct {
		name          string
		user          bool
		role          string
		levels        map[string]string // custom role created with these levels
		scope         string
		wantActive    bool
		wantNeedAdmin bool
	}{
		{name: "no users", wantActive: true},
		{name: "built-in admin", user: true, role: "admin"},
		{name: "user without any assignment", user: true, wantNeedAdmin: true},
		{name: "only viewer", user: true, role: "viewer", wantNeedAdmin: true},
		{name: "host-admin", user: true, role: "host-admin", wantNeedAdmin: true},
		{name: "only a scoped admin", user: true, role: "admin", scope: "host"},
		{name: "custom admin-equivalent role", user: true, role: "superops", levels: adminEquivalent},
		{name: "custom role without user management", user: true, role: "settings-only", levels: map[string]string{"settings": "manage"}, wantNeedAdmin: true},
		{name: "custom role named like admin", user: true, role: "Admin2", levels: map[string]string{"users": "view"}, wantNeedAdmin: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			database := newStateDB(t)
			if tt.levels != nil {
				if _, err := database.Exec(`INSERT INTO roles (name, display) VALUES (?, ?)`, tt.role, tt.role); err != nil {
					t.Fatalf("role: %v", err)
				}
				for area, level := range tt.levels {
					if _, err := database.Exec(`INSERT INTO role_levels (role_name, area, level) VALUES (?, ?, ?)`, tt.role, area, level); err != nil {
						t.Fatalf("level: %v", err)
					}
				}
			}
			if tt.user {
				if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'u1', 'x')`); err != nil {
					t.Fatalf("user: %v", err)
				}
			}
			if tt.role != "" {
				scope, host := "global", ""
				if tt.scope != "" {
					scope, host = tt.scope, "agent-1"
				}
				if _, err := database.Exec(`INSERT INTO role_assignments (user_id, role_name, scope_type, scope_host) VALUES ('u1', ?, ?, ?)`, tt.role, scope, host); err != nil {
					t.Fatalf("assignment: %v", err)
				}
			}
			// Every start decides again; a restart must not change the outcome.
			for range 2 {
				st, _, _, err := NewForBoot(ctx, database, Facts{})
				if err != nil {
					t.Fatalf("NewForBoot: %v", err)
				}
				if st.Active() != tt.wantActive {
					t.Fatalf("Active() = %v, want %v", st.Active(), tt.wantActive)
				}
				if !tt.wantActive && st.TryBeginCommit() {
					t.Fatal("commit accepted while setup is closed")
				}
			}
			need, err := NeedsAdmin(ctx, database)
			if err != nil {
				t.Fatalf("NeedsAdmin: %v", err)
			}
			if need != tt.wantNeedAdmin {
				t.Fatalf("NeedsAdmin() = %v, want %v", need, tt.wantNeedAdmin)
			}
		})
	}
}

func TestNoAdminNotice(t *testing.T) {
	ctx := context.Background()
	database := newStateDB(t)
	ns := notices.NewService(database)
	openNotice := func() *notices.Notice {
		t.Helper()
		if err := NoAdminNoticeCheck(database)(ctx, ns); err != nil {
			t.Fatalf("check: %v", err)
		}
		open, err := ns.Open(ctx)
		if err != nil {
			t.Fatalf("open notices: %v", err)
		}
		for i := range open {
			if open[i].Key == NoAdminNoticeKey {
				return &open[i]
			}
		}
		return nil
	}

	if openNotice() != nil {
		t.Fatal("notice raised on an install without accounts")
	}
	if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('u1', 'u1', 'x');
		INSERT INTO role_assignments (user_id, role_name, scope_type) VALUES ('u1', 'viewer', 'global')`); err != nil {
		t.Fatal(err)
	}
	n := openNotice()
	if n == nil {
		t.Fatal("no notice for accounts without an admin")
	}
	var audience string
	if err := database.QueryRow(`SELECT audience FROM notices WHERE id = ?`, n.ID).Scan(&audience); err != nil {
		t.Fatal(err)
	}
	if audience != string(notices.AudienceAll) || !strings.Contains(n.Body, RecoveryCommand) {
		t.Fatalf("notice audience %q, body %q: want everyone and the recovery command", audience, n.Body)
	}
	if _, err := database.Exec(`INSERT INTO role_assignments (user_id, role_name, scope_type) VALUES ('u1', 'admin', 'global')`); err != nil {
		t.Fatal(err)
	}
	if openNotice() != nil {
		t.Fatal("notice still open once an admin exists")
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

	expired := NewForced()
	expired.startedAt = time.Now().Add(-SetupWindow - time.Second)
	if expired.TryBeginCommit() {
		t.Fatal("commit accepted after the setup window expired")
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
