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

	list := func() string {
		rr := clusterCall(ctx, e.h.ListClusters, http.MethodGet, nil)
		return rr.Body.String()
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
	if after := list(); after != "[]\n" {
		t.Fatalf("after delete: %s", after)
	}
}

// A host-scoped hosts.view sees no clusters.
func TestListClustersNeedsGlobalView(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	if _, err := e.h.Clusters.Create(context.Background(), "local", "prod", ""); err != nil {
		t.Fatal(err)
	}
	e.roleScopes = map[string][]rbac.Scope{"viewer": {{Type: rbac.ScopeHost, Host: "local"}}}
	u := e.user("v", "viewer")
	ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: u.ID, Access: e.h.Roles.AccessFor(u.ID)})
	if rr := clusterCall(ctx, e.h.ListClusters, http.MethodGet, nil); rr.Body.String() != "[]\n" {
		t.Fatalf("host-scoped viewer sees %s", rr.Body)
	}
}
