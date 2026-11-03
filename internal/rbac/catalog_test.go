package rbac

import "testing"

// Metrics sits below View: a scrape-only role reads no audit log, and
// roles stored with Settings & audit: View keep the scrape.
func TestSettingsMetricsLevel(t *testing.T) {
	tests := []struct {
		level   string
		metrics bool
		audit   bool
	}{
		{"metrics", true, false},
		{"view", true, true},
		{"manage", true, true},
		{"none", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			perms := PermsFor(Levels{"settings": tt.level}, nil)
			if perms[PermMetricsView] != tt.metrics || perms[PermAuditView] != tt.audit {
				t.Errorf("metrics.view=%v audit.view=%v, want %v %v", perms[PermMetricsView], perms[PermAuditView], tt.metrics, tt.audit)
			}
		})
	}
}
