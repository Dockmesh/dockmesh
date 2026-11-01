package rbac

import "testing"

func builtinPerms(t *testing.T, name string) map[Perm]bool {
	t.Helper()
	r, ok := BuiltinRole(name)
	if !ok {
		t.Fatalf("no built-in role %s", name)
	}
	return PermsFor(r.Levels, r.Extras)
}

func TestBuiltinRoles(t *testing.T) {
	cases := []struct {
		role string
		perm Perm
		want bool
	}{
		{RoleAdmin, PermSystemUpgrade, true},
		{RoleAdmin, PermBackupsKeyExport, true},
		{RoleAdmin, PermUsersUpdate, true},

		{RoleHostAdmin, PermHostsCreate, true},
		{RoleHostAdmin, PermBackupsDownload, true},
		{RoleHostAdmin, PermContainersExec, true},
		{RoleHostAdmin, PermAuditView, true},
		{RoleHostAdmin, PermUsersView, false},
		{RoleHostAdmin, PermSystemUpdate, false},
		{RoleHostAdmin, PermBackupsKeyExport, false},

		{RoleDeployer, PermStacksDeploy, true},
		{RoleDeployer, PermStacksUpdate, true},
		{RoleDeployer, PermContainersUpdate, true},
		{RoleDeployer, PermStacksDelete, false},
		{RoleDeployer, PermContainersExec, false},
		{RoleDeployer, PermContainersEnv, false},

		{RoleOperator, PermStacksOperate, true},
		{RoleOperator, PermContainersExec, true},
		{RoleOperator, PermContainersLogs, true},
		{RoleOperator, PermStacksDeploy, false},
		{RoleOperator, PermProxyView, false},
		{RoleOperator, PermAuditView, false},

		{RoleViewer, PermContainersLogs, true},
		{RoleViewer, PermAuditView, true},
		{RoleViewer, PermContainersUpdate, false},
		{RoleViewer, PermContainersEnv, false},
		{RoleViewer, PermUsersView, false},
	}
	for _, tc := range cases {
		if got := builtinPerms(t, tc.role)[tc.perm]; got != tc.want {
			t.Errorf("%s holds %s = %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}
}

func TestLevelsAreCumulative(t *testing.T) {
	deploy := PermsFor(Levels{"stacks": "deploy"}, nil)
	for _, p := range []Perm{PermStacksView, PermStacksOperate, PermStacksDeploy} {
		if !deploy[p] {
			t.Errorf("stacks deploy lacks %s", p)
		}
	}
	if deploy[PermStacksDelete] {
		t.Error("stacks deploy grants delete")
	}
	if err := ValidateLevels(Levels{"stacks": "root"}, nil); err == nil {
		t.Error("unknown level accepted")
	}
	if err := ValidateLevels(nil, []string{"sudo"}); err == nil {
		t.Error("unknown extra accepted")
	}
}

func TestAccess(t *testing.T) {
	viewer := PermsFor(Levels{"stacks": "view", "containers": "view", "proxy": "view"}, nil)
	operator := PermsFor(Levels{"containers": "operate"}, nil)
	acc := &Access{Authenticated: true, Grants: []Grant{
		{Perms: viewer, Scopes: []Scope{{Type: ScopeHostTag, Value: "prod"}}},
		{Perms: operator, Scopes: []Scope{{Type: ScopeStack, Host: "h1", Value: "web"}}},
	}}
	prod := Resource{HostID: "h1", HostTags: []string{"prod"}}
	dev := Resource{HostID: "h2", HostTags: []string{"dev"}}
	web := Resource{HostID: "h1", StackName: "web"}
	cases := []struct {
		name string
		perm Perm
		res  Resource
		want bool
	}{
		{"view on tagged host", PermContainersView, prod, true},
		{"view on other host", PermContainersView, dev, false},
		{"restart in the stack", PermContainersUpdate, web, true},
		{"restart elsewhere on the host", PermContainersUpdate, Resource{HostID: "h1"}, false},
		{"restart the stack moved to another host", PermContainersUpdate, Resource{HostID: "h2", StackName: "web"}, false},
		{"restart the undeployed stack from a host-bound scope", PermContainersUpdate, Resource{StackName: "web"}, false},
		{"global-only perm from a scoped grant", PermProxyView, prod, false},
		{"base right", PermTokensCreate, dev, true},
	}
	for _, tc := range cases {
		if got := acc.AllowsOn(tc.perm, tc.res); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	if acc.Allows(PermProxyView) {
		t.Error("a scoped grant gave a global-only permission")
	}
	if acc.Unscoped() {
		t.Error("scoped access reported as unscoped")
	}
}

func TestRestrictNeverWidens(t *testing.T) {
	owner := &Access{Authenticated: true, Grants: []Grant{
		{Perms: PermsFor(Levels{"stacks": "deploy"}, nil), Scopes: []Scope{{Type: ScopeHost, Host: "h1"}}},
	}}
	tok := owner.Restrict(PermsFor(Levels{"stacks": "manage", "users": "manage"}, nil), Global)
	if tok.Allows(PermStacksDelete) || tok.Allows(PermUsersView) {
		t.Error("restriction added permissions the owner lacks")
	}
	if !tok.AllowsOn(PermStacksDeploy, Resource{HostID: "h1", StackName: "s"}) {
		t.Error("restriction lost the owner's permission")
	}
	narrow := owner.Restrict(PermsFor(Levels{"stacks": "view"}, nil), Scope{Type: ScopeStack, Host: "h1", Value: "a"})
	if narrow.AllowsOn(PermStacksView, Resource{HostID: "h1", StackName: "b"}) || narrow.AllowsOn(PermStacksDeploy, Resource{HostID: "h1", StackName: "a"}) {
		t.Error("restriction scope or role not applied")
	}
	if !narrow.AllowsOn(PermStacksView, Resource{HostID: "h1", StackName: "a"}) {
		t.Error("restricted token lost access inside the restriction")
	}
}

func TestHoldsOn(t *testing.T) {
	tags := func(h string) []string {
		if h == "h1" {
			return []string{"prod"}
		}
		return nil
	}
	acc := &Access{Authenticated: true, Grants: []Grant{{Perms: PermsFor(Levels{"stacks": "view"}, nil), Scopes: []Scope{{Type: ScopeHostTag, Value: "prod"}}}}}
	cases := []struct {
		scope Scope
		want  bool
	}{
		{Global, false},
		{Scope{Type: ScopeHostTag, Value: "prod"}, true},
		{Scope{Type: ScopeHostTag, Value: "dev"}, false},
		{Scope{Type: ScopeHost, Host: "h1"}, true},
		{Scope{Type: ScopeHost, Host: "h2"}, false},
		{Scope{Type: ScopeStack, Host: "h1", Value: "web"}, true},
		{Scope{Type: ScopeStack, Value: "web"}, false},
	}
	for _, tc := range cases {
		if got := acc.HoldsOn(PermStacksView, tc.scope, tags); got != tc.want {
			t.Errorf("HoldsOn %v = %v, want %v", tc.scope, got, tc.want)
		}
	}
}

func TestIdPAssignments(t *testing.T) {
	rules := []GroupRule{
		{Group: "ops", Role: RoleOperator},
		{Group: "web", Role: RoleDeployer, Scope: Scope{Type: ScopeStack, Host: "h1", Value: "web"}},
	}
	got := IdPAssignments("OIDC Corp", []string{"web", "ops"}, rules, RoleViewer, nil)
	if len(got) != 2 || got[0].Role != RoleOperator || !got[0].Scope.IsGlobal() || got[1].Scope.Value != "web" {
		t.Fatalf("matched groups: %+v", got)
	}
	none := IdPAssignments("OIDC Corp", []string{"other"}, rules, RoleViewer, nil)
	if len(none) != 1 || none[0].Role != RoleViewer || none[0].Via != "OIDC Corp default role" {
		t.Fatalf("default role: %+v", none)
	}
}

// Migrated custom roles get the highest levels their old permissions
// fully cover; nothing they didn't have before.
func TestLevelsCoveringNeverWidens(t *testing.T) {
	for _, old := range []map[Perm]bool{
		{PermStacksView: true, PermStacksDeploy: true},
		{PermContainersView: true, PermContainersLogs: true, PermContainersUpdate: true, PermContainersExec: true},
		{PermBackupsView: true, PermBackupsRestore: true, PermBackupsDownload: true},
		{PermUsersView: true, PermUsersCreate: true},
		{PermVolumesView: true, "volumes.read_file": true},
		{},
	} {
		have := LegacyEffective(old)
		levels, extras := levelsCovering(have)
		for p := range PermsFor(levels, extras) {
			if !have[p] {
				t.Errorf("%v: new role grants %s", old, p)
			}
		}
	}
	levels, _ := levelsCovering(LegacyEffective(map[Perm]bool{PermStacksView: true, PermContainersLogs: true, PermStacksDeploy: true, PermStacksUpdate: true, PermContainersUpdate: true}))
	if levels["stacks"] != "deploy" {
		t.Errorf("stacks level = %s, want deploy", levels["stacks"])
	}
}
