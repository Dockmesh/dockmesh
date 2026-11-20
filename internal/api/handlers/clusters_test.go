package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// swarmHosts is local + one agent, both nodes of one Swarm, and an agent
// outside it.
type swarmHosts struct{}

var swarmHostInfo = map[string]*agents.DaemonInfo{
	"local": {OS: "linux", Swarm: &agents.SwarmState{NodeID: "n1", NodeState: "active", Role: "manager", ClusterID: "c1"}},
	"a1":    {OS: "linux", Swarm: &agents.SwarmState{NodeID: "n2", NodeState: "active", Role: "worker"}},
	"a2":    {OS: "linux", Swarm: &agents.SwarmState{NodeState: "inactive"}},
}

func (swarmHosts) List(context.Context) ([]host.Info, error) {
	return []host.Info{{ID: "local", Name: "Local"}, {ID: "a1", Name: "w1"}, {ID: "a2", Name: "solo"}}, nil
}
func (swarmHosts) DaemonInfo(_ context.Context, id string) (*agents.DaemonInfo, error) {
	if i, ok := swarmHostInfo[id]; ok {
		return i, nil
	}
	return nil, agents.ErrNotFound
}
func (s swarmHosts) CurrentDaemonInfo(ctx context.Context, id string) (*agents.DaemonInfo, error) {
	return s.DaemonInfo(ctx, id)
}
func (swarmHosts) SwarmNodes(_ context.Context, id string) ([]agents.SwarmNode, error) {
	if id != "local" {
		return nil, errors.New("not a manager")
	}
	return []agents.SwarmNode{{ID: "n1", Role: "manager", Leader: true}, {ID: "n2", Role: "worker"}}, nil
}

func clusterCall(ctx context.Context, handler http.HandlerFunc, method string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "/", &buf).WithContext(ctx)
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

// Creating a cluster through the API links the confirmed nodes; later
// reconciles without a change leave the rows as they are.
func TestCreateClusterThroughAPI(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	u := e.user("root", "admin")
	ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: u.ID, Access: e.h.Roles.AccessFor(u.ID)})

	rr := clusterCall(ctx, e.h.ListClusterCandidates, http.MethodGet, nil)
	var cands []clusters.Candidate
	if err := json.Unmarshal(rr.Body.Bytes(), &cands); err != nil || len(cands) != 1 || cands[0].ClusterID != "c1" {
		t.Fatalf("candidates %s", rr.Body)
	}

	tests := []struct {
		name string
		body createClusterRequest
		want int
	}{
		{"worker host", createClusterRequest{HostID: "a1"}, http.StatusBadRequest},
		{"unknown host", createClusterRequest{HostID: "nope"}, http.StatusNotFound},
		{"no host", createClusterRequest{}, http.StatusBadRequest},
		{"manager", createClusterRequest{HostID: "local", Name: "prod"}, http.StatusCreated},
		{"again", createClusterRequest{HostID: "local", Name: "prod2"}, http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rr := clusterCall(ctx, e.h.CreateCluster, http.MethodPost, tt.body); rr.Code != tt.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}

	// The status carries check times; only the stored rows must not change.
	list := func() string {
		rr := clusterCall(ctx, e.h.ListClusters, http.MethodGet, nil)
		var cs []clusters.Cluster
		if err := json.Unmarshal(rr.Body.Bytes(), &cs); err != nil {
			t.Fatal(err)
		}
		for i := range cs {
			cs[i].Status = nil
		}
		out, err := json.Marshal(cs)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if _, err := e.db.Exec(`UPDATE cluster_memberships SET linked_at = '2000-01-01 00:00:00'`); err != nil {
		t.Fatal(err)
	}
	before := list()
	var got []clusters.Cluster
	if err := json.Unmarshal([]byte(before), &got); err != nil || len(got) != 1 || len(got[0].Members) != 2 {
		t.Fatalf("clusters %s", before)
	}
	for i := range 2 {
		if err := e.h.Clusters.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if after := list(); after != before {
			t.Fatalf("reconcile %d changed the clusters:\n%s\n%s", i, before, after)
		}
	}
	if !strings.Contains(before, "2000-01-01") {
		t.Fatalf("backdated rows missing: %s", before)
	}

	del := func(id string) int {
		req := httptest.NewRequest(http.MethodDelete, "/", nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rc))
		rr := httptest.NewRecorder()
		e.h.DeleteCluster(rr, req)
		return rr.Code
	}
	if code := del("c1"); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code := del("c1"); code != http.StatusNotFound {
		t.Fatalf("second delete: %d", code)
	}
	if after := list(); after != "[]" {
		t.Fatalf("after delete: %s", after)
	}
}

// A cluster-scoped hosts.view sees that cluster; a host scope on one of
// its nodes, a host tag or another cluster sees none.
func TestListClustersByScope(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	if _, err := e.h.Clusters.Create(context.Background(), "local", "prod", ""); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		scope rbac.Scope
		want  int
	}{
		{"cluster", rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"}, 1},
		{"global", rbac.Global, 1},
		{"node host", rbac.Scope{Type: rbac.ScopeHost, Host: "local"}, 0},
		{"host tag", rbac.Scope{Type: rbac.ScopeHostTag, Value: "prod"}, 0},
		{"other cluster", rbac.Scope{Type: rbac.ScopeCluster, Host: "c2"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.roleScopes = map[string][]rbac.Scope{"viewer": {tt.scope}}
			u := e.user("v-"+strings.ReplaceAll(tt.name, " ", "-"), "viewer")
			ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: u.ID, Access: e.h.Roles.AccessFor(u.ID)})
			var got []clusters.Cluster
			rr := clusterCall(ctx, e.h.ListClusters, http.MethodGet, nil)
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got) != tt.want {
				t.Fatalf("got %s, want %d clusters", rr.Body, tt.want)
			}
		})
	}
}

// A host scope shows its own host's membership with the cluster's name;
// a cluster scope alone shows no host.
func TestListClusterMembershipsByScope(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	if _, err := e.h.Clusters.Create(context.Background(), "local", "prod", ""); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		scope rbac.Scope
		want  []string
	}{
		{"global", rbac.Global, []string{"a1", "local"}},
		{"node host", rbac.Scope{Type: rbac.ScopeHost, Host: "a1"}, []string{"a1"}},
		{"other host", rbac.Scope{Type: rbac.ScopeHost, Host: "a2"}, nil},
		{"cluster", rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e.roleScopes = map[string][]rbac.Scope{"viewer": {tt.scope}}
			u := e.user("m-"+strings.ReplaceAll(tt.name, " ", "-"), "viewer")
			ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: u.ID, Access: e.h.Roles.AccessFor(u.ID)})
			var got []clusters.Membership
			rr := clusterCall(ctx, e.h.ListClusterMemberships, http.MethodGet, nil)
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got) != len(tt.want) {
				t.Fatalf("got %s, want hosts %v", rr.Body, tt.want)
			}
			for i, m := range got {
				if m.HostID != tt.want[i] || m.ClusterName != "prod" {
					t.Fatalf("got %s, want hosts %v", rr.Body, tt.want)
				}
			}
		})
	}
}

func clusterIDCall(ctx context.Context, handler http.HandlerFunc, method, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rc))
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

// The access preview names what a delete removes; the delete drops the
// cluster role from the cached access at once, and both answer 404 for
// an unknown cluster.
func TestDeleteClusterRemovesAccess(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	if _, err := e.h.Clusters.Create(context.Background(), "local", "prod", ""); err != nil {
		t.Fatal(err)
	}
	root := e.user("root", "admin")
	ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: root.ID, Access: e.h.Roles.AccessFor(root.ID)})
	v := e.user("carol", "viewer")
	if _, err := e.db.Exec(`DELETE FROM role_assignments WHERE user_id = ?`, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.Roles.AddAssignment(context.Background(), rbac.Assignment{UserID: v.ID, Role: "viewer", Scope: rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"}, Source: rbac.SourceManual}); err != nil {
		t.Fatal(err)
	}
	onCluster := func() bool {
		return e.h.Roles.AccessFor(v.ID).AllowsOn(rbac.PermHostsView, rbac.Resource{ClusterID: "c1"})
	}
	if !onCluster() {
		t.Fatal("cluster assignment does not grant hosts.view")
	}

	tests := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		id      string
		want    int
		check   func(t *testing.T, body []byte)
	}{
		{name: "access unknown", handler: e.h.ClusterAccess, method: http.MethodGet, id: "nope", want: http.StatusNotFound},
		{name: "access", handler: e.h.ClusterAccess, method: http.MethodGet, id: "c1", want: http.StatusOK, check: func(t *testing.T, body []byte) {
			var a clusterAccessView
			if err := json.Unmarshal(body, &a); err != nil || len(a.Assignments) != 1 || a.Assignments[0].Username != "carol" || !a.Complete {
				t.Fatalf("access %s", body)
			}
		}},
		{name: "delete unknown", handler: e.h.DeleteCluster, method: http.MethodDelete, id: "nope", want: http.StatusNotFound},
		{name: "delete", handler: e.h.DeleteCluster, method: http.MethodDelete, id: "c1", want: http.StatusNoContent},
		{name: "access after delete", handler: e.h.ClusterAccess, method: http.MethodGet, id: "c1", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		rr := clusterIDCall(ctx, tt.handler, tt.method, tt.id)
		if rr.Code != tt.want {
			t.Fatalf("%s: status %d, want %d: %s", tt.name, rr.Code, tt.want, rr.Body)
		}
		if tt.check != nil {
			tt.check(t, rr.Body.Bytes())
		}
	}
	if onCluster() {
		t.Fatal("cluster role still in the cached access after delete")
	}
}

// A caller with Hosts: Manage only holds clusters.create but neither
// users.view, tokens.manage_others nor the container permissions of the
// Admin role: the preview shows counts and the caller's own tokens only,
// and a delete that would remove access above the caller's own is
// refused with nothing deleted.
func TestDeleteClusterAccessAboveOwn(t *testing.T) {
	tests := []struct {
		name string
		seed func(e *authzEnv, ownerID, otherID string)
	}{
		{"admin assignment", func(e *authzEnv, _, otherID string) {
			if _, err := e.h.Roles.AddAssignment(context.Background(), rbac.Assignment{UserID: otherID, Role: "admin", Scope: rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"}, Source: rbac.SourceManual}); err != nil {
				t.Fatal(err)
			}
		}},
		{"admin group mapping", func(e *authzEnv, _, _ string) {
			for _, q := range []string{
				`INSERT INTO oidc_providers (id, slug, display_name, issuer_url, client_id, client_secret, default_role) VALUES (1, 'corp', 'Corp', 'https://idp.test', 'c', 's', 'viewer')`,
				`INSERT INTO oidc_provider_group_mappings (provider_id, group_value, role_name, scope_type, scope_host) VALUES (1, 'ops', 'admin', 'cluster', 'c1')`,
			} {
				if _, err := e.db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"other user's token", func(e *authzEnv, _, otherID string) {
			if _, err := e.db.Exec(`INSERT INTO api_tokens (token_prefix, token_hash, name, role, created_by_user_id, scope_type, scope_host) VALUES ('dmt_o', 'ho', 'theirs', 'viewer', ?, 'cluster', 'c1')`, otherID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAuthzEnv(t)
			e.h.Clusters = clusters.New(e.db, swarmHosts{})
			if _, err := e.h.Clusters.Create(context.Background(), "local", "prod", ""); err != nil {
				t.Fatal(err)
			}
			e.role("hosts-manager", []rbac.Perm{rbac.PermClustersCreate})
			ha := e.user("hosta", "hosts-manager")
			other := e.user("dave", "")
			if _, err := e.db.Exec(`INSERT INTO api_tokens (token_prefix, token_hash, name, role, created_by_user_id, scope_type, scope_host) VALUES ('dmt_m', 'hm', 'mine', 'viewer', ?, 'cluster', 'c1')`, ha.ID); err != nil {
				t.Fatal(err)
			}
			tt.seed(e, ha.ID, other.ID)
			ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: ha.ID, Access: e.h.Roles.AccessFor(ha.ID)})

			rr := clusterIDCall(ctx, e.h.ClusterAccess, http.MethodGet, "c1")
			var v clusterAccessView
			if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil || rr.Code != http.StatusOK {
				t.Fatalf("access %d %s", rr.Code, rr.Body)
			}
			total := v.Counts["assignments"] + v.Counts["mappings"] + v.Counts["tokens"]
			if v.Complete || len(v.Assignments)+len(v.Mappings) != 0 || len(v.Tokens) != 1 || v.Tokens[0].Name != "mine" || total != 2 {
				t.Fatalf("host admin sees %s", rr.Body)
			}

			rr = clusterIDCall(ctx, e.h.DeleteCluster, http.MethodDelete, "c1")
			if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "cluster_access_above_own") {
				t.Fatalf("delete %d %s", rr.Code, rr.Body)
			}
			a, err := e.h.Clusters.AccessOf(context.Background(), "c1")
			if err != nil {
				t.Fatalf("cluster gone after refused delete: %v", err)
			}
			if c := a.Counts(); c["assignments"]+c["mappings"]+c["tokens"] != 2 {
				t.Fatalf("access changed by refused delete: %v", c)
			}
		})
	}
}

// Without tokens.manage_others, a user owns the tokens they created, an
// API token only itself, and a caller without an owner none, also not
// tokens without an owner.
func TestClusterTokenOwnership(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	if _, err := e.h.Clusters.Create(context.Background(), "local", "prod", ""); err != nil {
		t.Fatal(err)
	}
	e.role("hosts-manager", []rbac.Perm{rbac.PermClustersCreate})
	u := e.user("hosta", "hosts-manager")
	access := e.h.Roles.AccessFor(u.ID)
	insert := func(owner any) int64 {
		var id int64
		if err := e.db.QueryRow(`INSERT INTO api_tokens (token_prefix, token_hash, name, role, created_by_user_id, scope_type, scope_host)
			VALUES ('dmt_' || hex(randomblob(4)), hex(randomblob(8)), 'tok', 'viewer', ?, 'cluster', 'c1') RETURNING id`, owner).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	tests := []struct {
		name      string
		owner     any
		identity  func(tokenID int64) middleware.Identity
		wantNamed bool
	}{
		{"user's own token", u.ID, func(int64) middleware.Identity { return middleware.Identity{UserID: u.ID, Access: access} }, true},
		{"the calling token itself", u.ID, func(id int64) middleware.Identity {
			return middleware.Identity{APITokenID: id, TokenOwnerID: u.ID, Access: access}
		}, true},
		{"sibling token of the calling token", u.ID, func(id int64) middleware.Identity {
			return middleware.Identity{APITokenID: id + 1000, TokenOwnerID: u.ID, Access: access}
		}, false},
		{"ownerless caller, ownerless token", nil, func(int64) middleware.Identity { return middleware.Identity{Access: access} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := e.db.Exec(`DELETE FROM api_tokens`); err != nil {
				t.Fatal(err)
			}
			id := insert(tt.owner)
			ctx := middleware.WithIdentity(context.Background(), tt.identity(id))
			rr := clusterIDCall(ctx, e.h.ClusterAccess, http.MethodGet, "c1")
			var v clusterAccessView
			if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil || v.Counts["tokens"] != 1 {
				t.Fatalf("access %d %s", rr.Code, rr.Body)
			}
			if named := len(v.Tokens) == 1; named != tt.wantNamed {
				t.Fatalf("token named %v, want %v: %s", named, tt.wantNamed, rr.Body)
			}
			err := e.h.checkClusterAccess(httptest.NewRequest(http.MethodDelete, "/", nil).WithContext(ctx), "c1", clusters.Access{Tokens: []clusters.ScopedToken{{ID: id, OwnerID: ownerString(tt.owner)}}})
			if (err == nil) != tt.wantNamed {
				t.Fatalf("delete check %v, want allowed %v", err, tt.wantNamed)
			}
		})
	}
}

func ownerString(v any) string {
	s, _ := v.(string)
	return s
}
