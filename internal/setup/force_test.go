package setup

import (
	"context"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/notices"
)

func TestNewForBootForceFlag(t *testing.T) {
	tests := []struct {
		name         string
		force        bool
		admin        bool
		raisedBefore bool
		wantActive   bool
		wantIgnored  bool
		wantNotice   bool
	}{
		{name: "flag with admin", force: true, admin: true, wantActive: false, wantIgnored: true, wantNotice: true},
		{name: "flag without admin", force: true, admin: false, wantActive: true},
		{name: "flag removed resolves notice", force: false, admin: true, raisedBefore: true, wantActive: false},
		{name: "no flag no admin", force: false, admin: false, wantActive: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			database := newStateDB(t)
			if tt.admin {
				if _, err := database.Exec(`INSERT INTO users (id, username, password, role) VALUES ('u1', 'u1', 'x', 'admin')`); err != nil {
					t.Fatalf("user: %v", err)
				}
			}
			ns := notices.NewService(database)
			if tt.raisedBefore {
				if err := ForceNoticeCheck(true, "/etc/x.env")(ctx, ns); err != nil {
					t.Fatalf("raise: %v", err)
				}
			}

			st, ignored, err := NewForBoot(ctx, database, tt.force)
			if err != nil {
				t.Fatalf("NewForBoot: %v", err)
			}
			if st.Active() != tt.wantActive {
				t.Errorf("Active() = %v, want %v", st.Active(), tt.wantActive)
			}
			if ignored != tt.wantIgnored {
				t.Errorf("forceIgnored = %v, want %v", ignored, tt.wantIgnored)
			}
			if !tt.wantActive && st.TryBeginCommit() {
				t.Error("commit accepted while setup is closed")
			}

			if err := ForceNoticeCheck(ignored, "/srv/dm/dockmesh.env")(ctx, ns); err != nil {
				t.Fatalf("notice check: %v", err)
			}
			open, err := ns.Open(ctx)
			if err != nil {
				t.Fatalf("open notices: %v", err)
			}
			var found *notices.Notice
			for i := range open {
				if open[i].Key == ForceIgnoredNoticeKey {
					found = &open[i]
				}
			}
			if (found != nil) != tt.wantNotice {
				t.Fatalf("notice open = %v, want %v", found != nil, tt.wantNotice)
			}
			if found != nil && !strings.Contains(found.Body, "/srv/dm/dockmesh.env") {
				t.Errorf("notice body does not name the env file: %q", found.Body)
			}
		})
	}
}

func TestForceNoticeUnknownEnvFile(t *testing.T) {
	ctx := context.Background()
	ns := notices.NewService(newStateDB(t))
	if err := ForceNoticeCheck(true, "")(ctx, ns); err != nil {
		t.Fatalf("raise: %v", err)
	}
	open, err := ns.Open(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("open = %v, %v", open, err)
	}
	if !strings.Contains(open[0].Body, DefaultEnvFile) || !strings.Contains(open[0].Body, "DOCKMESH_SETUP_FORCE") {
		t.Errorf("body = %q", open[0].Body)
	}
}
