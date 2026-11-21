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

// A cluster scope covers the cluster's own resources and nothing on its
// nodes; host and host tag scopes never reach the cluster.
func TestClusterScope(t *testing.T) {
	hostsView := PermsFor(Levels{"hosts": "view", "containers": "view"}, nil)
	acc := &Access{Grants: []Grant{
		{Perms: hostsView, Scopes: []Scope{{Type: ScopeCluster, Host: "c1"}}},
		{Perms: hostsView, Scopes: []Scope{{Type: ScopeHost, Host: "m1"}}},
	}}
	cases := []struct {
		name string
		res  Resource
		want bool
	}{
		{"own cluster", Resource{ClusterID: "c1"}, true},
		{"other cluster", Resource{ClusterID: "c2"}, false},
		{"node host by its host grant", Resource{HostID: "m1"}, true},
		{"node host without host grant", Resource{HostID: "m2"}, false},
		{"empty resource", Resource{}, false},
	}
	for _, tc := range cases {
		if got := acc.AllowsOn(PermHostsView, tc.res); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	hostOnly := &Access{Grants: []Grant{
		{Perms: hostsView, Scopes: []Scope{{Type: ScopeHost, Host: "m1"}}},
		{Perms: hostsView, Scopes: []Scope{{Type: ScopeHostTag, Value: "prod"}}},
		{Perms: hostsView, Scopes: []Scope{{Type: ScopeStack, Value: "web"}}},
	}}
	if hostOnly.AllowsOn(PermHostsView, Resource{ClusterID: "c1"}) {
		t.Error("a host, tag or stack grant reached a cluster")
	}
	clusterOnly := &Access{Grants: []Grant{{Perms: hostsView, Scopes: []Scope{{Type: ScopeCluster, Host: "c1"}}}}}
	if clusterOnly.AllowsNear(PermContainersView, "m1", nil) || clusterOnly.AllowsOn(PermContainersView, Resource{HostID: "m1"}) {
		t.Error("a cluster grant reached a node host")
	}
	clusterManage := &Access{Grants: []Grant{{Perms: PermsFor(Levels{"hosts": "manage"}, nil), Scopes: []Scope{{Type: ScopeCluster, Host: "c1"}}}}}
	if clusterManage.Allows(PermClustersCreate) {
		t.Error("a cluster grant gave a global-only permission")
	}
	tok := (&Access{Grants: []Grant{{Perms: hostsView, Scopes: []Scope{Global}}}}).Restrict(hostsView, Scope{Type: ScopeCluster, Host: "c1"})
	if !tok.AllowsOn(PermHostsView, Resource{ClusterID: "c1"}) || tok.AllowsOn(PermHostsView, Resource{ClusterID: "c2"}) || tok.AllowsOn(PermHostsView, Resource{HostID: "m1"}) {
		t.Error("a token restricted to a cluster matched outside it")
	}
}

func TestScopeContainsCluster(t *testing.T) {
	c1 := Scope{Type: ScopeCluster, Host: "c1"}
	cases := []struct {
		c, s Scope
		want bool
	}{
		{c1, c1, true},
		{c1, Scope{Type: ScopeCluster, Host: "c2"}, false},
		{c1, Scope{Type: ScopeHost, Host: "c1"}, false},
		{Scope{Type: ScopeHost, Host: "c1"}, c1, false},
		{Scope{Type: ScopeHostTag, Value: "prod"}, c1, false},
		{Global, c1, true},
	}
	for _, tc := range cases {
		if got := ScopeContains(tc.c, tc.s, func(string) []string { return []string{"prod"} }); got != tc.want {
			t.Errorf("%v contains %v: %v, want %v", tc.c, tc.s, got, tc.want)
		}
	}
}

func TestScopeValidate(t *testing.T) {
	cases := []struct {
		s  Scope
		ok bool
	}{
		{Scope{Type: ScopeCluster, Host: "c1"}, true},
		{Scope{Type: ScopeCluster}, false},
		{Scope{Type: ScopeCluster, Host: "c1", Value: "x"}, false},
		{Scope{Type: "swarm", Host: "c1"}, false},
	}
	for _, tc := range cases {
		if err := tc.s.Validate(); (err == nil) != tc.ok {
			t.Errorf("%+v: %v, want ok=%v", tc.s, err, tc.ok)
		}
	}
}

// A stack scope's host may name the Swarm cluster the stack is deployed
// to; a host scope still never reaches a stack on a cluster.
func TestStackScopeOnClusterTarget(t *testing.T) {
	deploy := PermsFor(Levels{"stacks": "deploy"}, nil)
	onCluster := Resource{ClusterID: "c1", StackName: "web"}
	cases := []struct {
		name  string
		scope Scope
		res   Resource
		want  bool
	}{
		{"stack on its cluster", Scope{Type: ScopeStack, Host: "c1", Value: "web"}, onCluster, true},
		{"stack without target", Scope{Type: ScopeStack, Value: "web"}, onCluster, true},
		{"stack on another cluster", Scope{Type: ScopeStack, Host: "c2", Value: "web"}, onCluster, false},
		{"other stack on the cluster", Scope{Type: ScopeStack, Host: "c1", Value: "db"}, onCluster, false},
		{"cluster scope", Scope{Type: ScopeCluster, Host: "c1"}, onCluster, true},
		{"host scope named like the cluster", Scope{Type: ScopeHost, Host: "c1"}, onCluster, false},
		{"stack on host scope", Scope{Type: ScopeStack, Host: "m1", Value: "web"}, onCluster, false},
		{"cluster-named stack scope on a host stack", Scope{Type: ScopeStack, Host: "c1", Value: "web"}, Resource{HostID: "m1", StackName: "web"}, false},
		{"host stack", Scope{Type: ScopeStack, Host: "m1", Value: "web"}, Resource{HostID: "m1", StackName: "web"}, true},
	}
	for _, tc := range cases {
		acc := &Access{Grants: []Grant{{Perms: deploy, Scopes: []Scope{tc.scope}}}}
		if got := acc.AllowsOn(PermStacksDeploy, tc.res); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	c1 := Scope{Type: ScopeCluster, Host: "c1"}
	noTags := func(string) []string { return nil }
	if !ScopeContains(c1, Scope{Type: ScopeStack, Host: "c1", Value: "web"}, noTags) {
		t.Error("a cluster scope must contain a stack scope on that cluster")
	}
	if ScopeContains(c1, Scope{Type: ScopeStack, Value: "web"}, noTags) {
		t.Error("a cluster scope must not contain a stack scope without target")
	}
	if ScopeContains(c1, Scope{Type: ScopeStack, Host: "c2", Value: "web"}, noTags) {
		t.Error("a cluster scope must not contain a stack scope on another cluster")
	}
}
