package alerts

import (
	"testing"

	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

func TestAlertNotification(t *testing.T) {
	tests := []struct {
		severity string
		hostID   string
		want     notifications.Severity
		unscoped bool
	}{
		{"critical", "local", notifications.SevError, false},
		{"warning", "agent-1", notifications.SevWarning, false},
		{"info", "", notifications.SevInfo, true},
		{"", "local", notifications.SevWarning, false},
	}
	for _, tt := range tests {
		t.Run(tt.severity+"/"+tt.hostID, func(t *testing.T) {
			in := alertNotification(&Rule{Name: "cpu", Severity: tt.severity}, tt.hostID, "msg")
			if in.Severity != tt.want {
				t.Errorf("severity = %q, want %q", in.Severity, tt.want)
			}
			if in.Perm != rbac.PermAlertsView || in.HostID != tt.hostID || in.Unscoped != tt.unscoped || in.UserID != "" {
				t.Errorf("audience: perm %q host %q unscoped %v user %q", in.Perm, in.HostID, in.Unscoped, in.UserID)
			}
		})
	}
}
