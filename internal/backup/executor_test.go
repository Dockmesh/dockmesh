package backup

import (
	"testing"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

func TestRunNotificationAudience(t *testing.T) {
	tests := []struct {
		name         string
		job          Job
		wantPerm     rbac.Perm
		wantHost     string
		wantUnscoped bool
	}{
		{"local volume job", Job{Sources: []Source{{Type: "volume", Name: "pg"}}}, rbac.PermBackupsView, "local", false},
		{"agent host job", Job{HostID: "h1", Sources: []Source{{Type: "stack", Name: "web"}}}, rbac.PermBackupsView, "h1", false},
		{"system job", Job{Sources: []Source{{Type: "system"}}}, rbac.PermSystemUpdate, "local", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := runNotification(&tt.job, 7)
			if in.Perm != tt.wantPerm || in.HostID != tt.wantHost || in.Unscoped != tt.wantUnscoped {
				t.Errorf("got perm %q host %q unscoped %v", in.Perm, in.HostID, in.Unscoped)
			}
			if in.Link != "/backups/runs/7" || in.UserID != "" {
				t.Errorf("link %q user %q", in.Link, in.UserID)
			}
		})
	}
}
