package rbac

import "testing"

// A token restricted to a role keeps the base rights only while the
// narrowed access administers the whole instance.
func TestRestrictKeepsBaseRightsOnlyForAdmins(t *testing.T) {
	admin, _ := BuiltinRole(RoleAdmin)
	viewer, _ := BuiltinRole(RoleViewer)
	adminPerms := PermsFor(admin.Levels, admin.Extras)
	viewerPerms := PermsFor(viewer.Levels, viewer.Extras)
	usersPerms := PermsFor(Levels{"users": "manage"}, nil)
	host := Scope{Type: ScopeHost, Host: "edge1"}
	owner := func(perms map[Perm]bool, s Scope) *Access {
		return &Access{Authenticated: true, Grants: []Grant{{Perms: perms, Scopes: []Scope{s}}}}
	}
	tests := []struct {
		name  string
		owner *Access
		role  map[Perm]bool
		scope Scope
		want  bool
	}{
		{"admin token of an admin", owner(adminPerms, Global), adminPerms, Global, true},
		{"viewer token of an admin", owner(adminPerms, Global), viewerPerms, Global, false},
		{"admin token on one host", owner(adminPerms, Global), adminPerms, host, false},
		{"admin token of a demoted owner", owner(viewerPerms, Global), adminPerms, Global, false},
		{"admin token of a host admin", owner(adminPerms, host), adminPerms, Global, false},
		// Users & roles: Manage carries tokens.manage_others, but without the
		// base rights the token routes stay closed to it.
		{"users-manage token of an admin", owner(adminPerms, Global), usersPerms, Global, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.owner.Restrict(tt.role, tt.scope)
			for _, p := range []Perm{PermTokensView, PermTokensCreate, PermSystemView} {
				if got.Allows(p) != tt.want {
					t.Errorf("%s allowed = %v, want %v", p, got.Allows(p), tt.want)
				}
			}
		})
	}
}
