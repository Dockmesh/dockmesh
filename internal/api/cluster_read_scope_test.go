package api

import (
	"encoding/json"
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

// The service log stream checks containers.logs on the cluster in the
// path, like the read routes: a role on another cluster, a host, a host
// tag or a stack is refused even when it holds containers.logs there.
func TestClusterServiceLogsFollowClusterScope(t *testing.T) {
	authSvc, roles, h := newMatrixEnv(t)
	router := NewRouter(h, authSvc, nil, true, nil)
	for _, b := range rbac.BuiltinRoles {
		holds := rbac.PermsFor(b.Levels, b.Extras)[rbac.PermContainersLogs]
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
			name := "logs-" + b.Name + "-" + sc.tag
			bearer := signIn(t, authSvc, roles, name, b.Name, sc.scope)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/ws/ticket?for=containers.logs", nil)
			req.Header.Set("Authorization", "Bearer "+bearer)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			code := rr.Code
			if code == http.StatusOK {
				var tk struct{ Ticket string }
				if err := json.Unmarshal(rr.Body.Bytes(), &tk); err != nil {
					t.Fatal(err)
				}
				req = httptest.NewRequest(http.MethodGet, "/api/v1/ws/clusters/c1/services/web/logs?ticket="+tk.Ticket, nil)
				rr = httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				code = rr.Code
			}
			want := sc.reach && holds
			if denied := code == http.StatusForbidden; denied == want {
				t.Errorf("%s: %d %s, want allowed=%v", name, code, strings.TrimSpace(rr.Body.String()), want)
			}
		}
	}
}
