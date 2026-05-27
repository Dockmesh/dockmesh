package rbac

import "testing"

func TestAllowed(t *testing.T) {
	cases := []struct {
		role string
		perm Perm
		want bool
	}{
		// Admin gets everything.
		{"admin", PermContainersView, true},
		{"admin", PermUsersUpdate, true},
		{"admin", PermStacksUpdate, true},
		{"admin", PermSystemUpgrade, true},
		{"admin", PermAuditWrite, true},

		// Operator: lifecycle + logs/exec + deploy. NO compose edits,
		// no destruction, no admin domains.
		{"operator", PermContainersView, true},
		{"operator", PermContainersUpdate, true},
		{"operator", PermContainersExec, true},
		{"operator", PermStacksDeploy, true},
		{"operator", PermStacksUpdate, false}, // cannot edit compose
		{"operator", PermStacksCreate, false}, // cannot author new stacks (deployer-tier needed)
		{"operator", PermUsersUpdate, false},
		{"operator", PermImagesCreate, false}, // cannot pull new images
		{"operator", PermAuditWrite, false},

		// Deployer: above operator + create on stacks/images/volumes.
		{"deployer", PermStacksCreate, true},
		{"deployer", PermStacksDeploy, true},
		{"deployer", PermImagesCreate, true},
		{"deployer", PermVolumesCreate, true},
		{"deployer", PermUsersUpdate, false},   // still no user mgmt
		{"deployer", PermSystemUpgrade, false}, // not host-admin

		// Host-admin: above deployer + host management + everything on
		// assigned hosts. Still NO user/role/audit-write/system-upgrade.
		{"host-admin", PermHostsCreate, true},
		{"host-admin", PermHostsDelete, true},
		{"host-admin", PermBackupsRestore, true},
		{"host-admin", PermUsersCreate, false},
		{"host-admin", PermAuditWrite, false},
		{"host-admin", PermSystemUpgrade, false},

		// Viewer: read-only across the fleet.
		{"viewer", PermContainersView, true},
		{"viewer", PermAuditView, true},
		{"viewer", PermMetricsView, true},
		{"viewer", PermContainersUpdate, false},
		{"viewer", PermContainersExec, false},
		{"viewer", PermStacksDeploy, false},
		{"viewer", PermUsersUpdate, false},

		// Unknown role / empty role: nothing.
		{"", PermContainersView, false},
		{"unknown", PermContainersView, false},
	}
	for _, tc := range cases {
		got := Allowed(tc.role, tc.perm)
		if got != tc.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}
}
