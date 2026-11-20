package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/topology"
)

var (
	graphNets = []agents.SwarmNetwork{
		{ID: "ing", Name: "ingress", Driver: "overlay", Ingress: true},
		{ID: "n1", Name: "app_front", Driver: "overlay", Stack: "app"},
		{ID: "n2", Name: "app_back", Driver: "overlay", Stack: "app", Internal: true},
	}
	graphSvcs = []agents.SwarmService{
		{ID: "s1", Name: "app_web", Stack: "app", Networks: []string{"n1", "n2", "ing", "n1"}},
		{ID: "s2", Name: "app_db", Stack: "app", Networks: []string{"app_back", "gone"}},
		{ID: "s3", Name: "solo"},
	}
)

// Services link to the networks they attach to by id or by name; unknown
// references and duplicates are dropped, ingress is a system network.
func TestClusterGraph(t *testing.T) {
	g := clusterGraph(graphNets, graphSvcs)
	want := []topology.Link{
		{NetworkID: "ing", ServiceID: "s1"},
		{NetworkID: "n1", ServiceID: "s1"},
		{NetworkID: "n2", ServiceID: "s1"},
		{NetworkID: "n2", ServiceID: "s2"},
	}
	if !reflect.DeepEqual(g.Links, want) {
		t.Fatalf("links %+v", g.Links)
	}
	if len(g.Containers) != 0 || len(g.Networks) != 3 || !g.Networks[0].System || g.Networks[1].System || g.Networks[2].Scope != "swarm" || !g.Networks[2].Internal {
		t.Fatalf("networks %+v containers %+v", g.Networks, g.Containers)
	}
}

func TestAttachServices(t *testing.T) {
	got := attachServices(graphNets, graphSvcs)
	names := map[string][]string{}
	for _, n := range got {
		for _, s := range n.Services {
			names[n.ID] = append(names[n.ID], s.Name)
		}
	}
	want := map[string][]string{"ing": {"app_web"}, "n1": {"app_web"}, "n2": {"app_db", "app_web"}}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v", names)
	}
	if none := attachServices(graphNets, nil); len(none) != 3 || none[1].Services != nil {
		t.Fatalf("without services %+v", none)
	}
}

type volumeHost struct {
	host.Host
	id   string
	vols []any
	err  error
}

func (f *volumeHost) ID() string                                 { return f.id }
func (f *volumeHost) Name() string                               { return f.id + "-name" }
func (f *volumeHost) ListVolumes(context.Context) ([]any, error) { return f.vols, f.err }

func vol(name, stack, driver string) map[string]any {
	labels := map[string]any{}
	if stack != "" {
		labels["com.docker.stack.namespace"] = stack
	}
	return map[string]any{"Name": name, "Driver": driver, "Labels": labels}
}

// Stack volumes are grouped per stack with the member hosts that hold a
// copy; volumes of no Swarm stack and hosts outside the cluster are left
// out, an offline member is listed as unreachable.
func TestClusterVolumes(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	if _, err := e.db.Exec(`INSERT INTO clusters (id, name, origin_host) VALUES ('c1', 'prod', 'm1');
		INSERT INTO cluster_memberships (host_id, cluster_id, node_id, role) VALUES
		('m1', 'c1', 'n1', 'manager'), ('m2', 'c1', 'n2', 'manager'), ('w1', 'c1', 'n3', 'worker')`); err != nil {
		t.Fatal(err)
	}
	e.h.Hosts = host.NewFixedRegistry(
		&volumeHost{id: "m1", vols: []any{vol("app_data", "app", "local"), vol("other", "", "local"), vol("app_nfs", "app", "local")}},
		&volumeHost{id: "m2", vols: []any{vol("app_data", "app", "local"), vol("db_pg", "db", "local")}},
		&volumeHost{id: "w1", err: errors.New("agent offline")},
		&volumeHost{id: "solo", vols: []any{vol("app_data", "app", "local")}},
	)
	u := e.user("root", "admin")
	ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: u.ID, Access: e.h.Roles.AccessFor(u.ID)})
	call := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rc))
		rr := httptest.NewRecorder()
		e.h.ClusterVolumes(rr, req)
		return rr
	}
	if rr := call("nope"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown cluster: %d", rr.Code)
	}
	rr := call("c1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var got clusterVolumesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	spread := map[string][]string{}
	for _, v := range got.Volumes {
		for _, n := range v.Nodes {
			spread[v.Stack+"/"+v.Name] = append(spread[v.Stack+"/"+v.Name], n.HostID)
		}
	}
	want := map[string][]string{"app/app_data": {"m1", "m2"}, "app/app_nfs": {"m1"}, "db/db_pg": {"m2"}}
	if !reflect.DeepEqual(spread, want) {
		t.Fatalf("spread %v", spread)
	}
	if !reflect.DeepEqual(got.ReadHosts, []string{"m1", "m2"}) || len(got.Unreachable) != 1 || got.Unreachable[0].HostID != "w1" {
		t.Fatalf("read %v unreachable %+v", got.ReadHosts, got.Unreachable)
	}
}
