package alerts

import (
	"testing"

	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

func TestAlertNotification(t *testing.T) {
	tests := []struct {
		severity string
		want     notifications.Severity
	}{
		{"critical", notifications.SevError},
		{"warning", notifications.SevWarning},
		{"info", notifications.SevInfo},
		{"", notifications.SevWarning},
	}
	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			in := alertNotification(&Rule{Name: "cpu", Severity: tt.severity}, "msg")
			if in.Severity != tt.want {
				t.Errorf("severity = %q, want %q", in.Severity, tt.want)
			}
			if in.Perm != rbac.PermAlertsView || in.HostID != "local" || in.UserID != "" {
				t.Errorf("audience: perm %q host %q user %q", in.Perm, in.HostID, in.UserID)
			}
		})
	}
}
