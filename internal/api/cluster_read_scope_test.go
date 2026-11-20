package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// The cluster read routes check their permission on the cluster in the
// path: every built-in role on that cluster gets through exactly when it
// holds the permission; the same role on another cluster, on a host, a
// host tag or a stack never does.
func TestClusterReadRoutesFollowClusterScope(t *testing.T) {
	authSvc, roles, h := newMatrixEnv(t)
	router := NewRouter(h, authSvc, nil, true, nil)

	type caller struct {
		name   string
		perms  map[rbac.Perm]bool
		reach  bool
		bearer string
	}
	var callers []caller
	for _, b := range rbac.BuiltinRoles {
		perms := rbac.PermsFor(b.Levels, b.Extras)
		for _, sc := range []struct {
			tag   string
			scope rbac.Scope
			reach bool
		}{
			{"cluster", rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"}, true},
			{"other-cluster", rbac.Scope{Type: rbac.ScopeCluster, Host: "c2"}, false},
			{"host", rbac.Scope{Type: rbac.ScopeHost, Host: "local"}, false},
			{"tag", rbac.Scope{Type: rbac.ScopeHostTag, Value: "prod"}, false},
			{"stack", rbac.Scope{Type: rbac.ScopeStack, Host: "local", Value: "web"}, false},
		} {
			name := b.Name + "-" + sc.tag
			callers = append(callers, caller{name: name, perms: perms, reach: sc.reach,
				bearer: signIn(t, authSvc, roles, name, b.Name, sc.scope)})
		}
	}

	routes := []struct {
		path string
		perm rbac.Perm
	}{
		{"/api/v1/clusters/c1/nodes", rbac.PermHostsView},
		{"/api/v1/clusters/c1/services", rbac.PermStacksView},
		{"/api/v1/clusters/c1/services/web_app", rbac.PermStacksView},
		{"/api/v1/clusters/c1/tasks?service=web_app", rbac.PermStacksView},
		{"/api/v1/clusters/c1/networks", rbac.PermNetworksView},
	}
	for _, rt := range routes {
		for _, c := range callers {
			req := httptest.NewRequest(http.MethodGet, rt.path, nil)
			req.Header.Set("Authorization", "Bearer "+c.bearer)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			want := c.reach && c.perms[rt.perm]
			denied := rr.Code == http.StatusForbidden
			if denied == want {
				t.Errorf("GET %s as %s: %d %s, want allowed=%v", rt.path, c.name, rr.Code, strings.TrimSpace(rr.Body.String()), want)
			}
		}
	}
}
