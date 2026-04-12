package rbac

import "testing"

func TestAllowed(t *testing.T) {
	cases := []struct {
		role string
		perm Perm
		want bool
	}{
		{"admin", PermRead, true},
		{"admin", PermUserManage, true},
		{"admin", PermStackWrite, true},
		{"operator", PermRead, true},
		{"operator", PermContainerControl, true},
		{"operator", PermStackDeploy, true},
		{"operator", PermContainerExec, true},
		{"operator", PermStackWrite, false}, // cannot edit compose
		{"operator", PermUserManage, false},
		{"operator", PermImageWrite, false},
		{"viewer", PermRead, true},
		{"viewer", PermContainerControl, false},
		{"viewer", PermContainerExec, false},
		{"viewer", PermStackDeploy, false},
		{"viewer", PermUserManage, false},
		{"", PermRead, false},
		{"unknown", PermRead, false},
	}
	for _, tc := range cases {
		got := Allowed(tc.role, tc.perm)
		if got != tc.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}
}
