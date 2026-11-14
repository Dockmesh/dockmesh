package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/topology"
)

type topologyHost struct {
	host.Host
	id    string
	graph topology.Graph
	err   error
}

func (f *topologyHost) ID() string   { return f.id }
func (f *topologyHost) Name() string { return f.id + "-name" }
func (f *topologyHost) Topology(context.Context) (topology.Graph, error) {
	return f.graph, f.err
}

// graphOf is a host running stack "web" (own network, also on the shared
// "edge" network), stack "db" and a standalone container on bridge.
func graphOf(prefix string) topology.Graph {
	id := func(s string) string { return prefix + s }
	return topology.Graph{
		Networks: []topology.Network{
			{ID: id("n-web"), Name: "web_default", Stack: "web"},
			{ID: id("n-db"), Name: "db_default", Stack: "db"},
			{ID: id("n-edge"), Name: "edge"},
			{ID: id("n-bridge"), Name: "bridge", System: true},
		},
		Containers: []topology.Container{
			{ID: id("c-web"), Name: "web-app-1", Stack: "web"},
			{ID: id("c-db"), Name: "db-pg-1", Stack: "db"},
			{ID: id("c-solo"), Name: "solo"},
		},
		Links: []topology.Link{
			{NetworkID: id("n-web"), ContainerID: id("c-web")},
			{NetworkID: id("n-edge"), ContainerID: id("c-web")},
			{NetworkID: id("n-db"), ContainerID: id("c-db")},
			{NetworkID: id("n-edge"), ContainerID: id("c-db")},
			{NetworkID: id("n-bridge"), ContainerID: id("c-solo")},
		},
	}
}

func TestTopologyFollowsTargetAndScope(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO agents (id, name, status) VALUES ('agent-1', 'agent-1', 'online'), ('agent-old', 'agent-old', 'online')`); err != nil {
		t.Fatal(err)
	}
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "agent-1", []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	e.h.Hosts = host.NewFixedRegistry(
		&topologyHost{id: "local", graph: graphOf("l-")},
		&topologyHost{id: "agent-1", graph: graphOf("a-")},
		&topologyHost{id: "agent-old", err: host.ErrAgentNoTopology},
	)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermNetworksView)).Get("/networks/topology", e.h.GetTopology)
	})
	e.role("web-viewer", []rbac.Perm{rbac.PermNetworksView}, rbac.Scope{Type: rbac.ScopeStack, Host: "agent-1", Value: "web"})
	e.user("boss", "admin")
	tagged := e.user("tagged", "viewer")
	e.scopeUser(tagged.ID, "prod")
	e.user("webby", "web-viewer")

	tests := []struct {
		name, user, query string
		status            int
		target            string
		containers        []string
		networks          []string
		links             int
	}{
		{"no host means the server", "boss", "", http.StatusOK, "local",
			[]string{"l-c-web", "l-c-db", "l-c-solo"}, []string{"l-n-web", "l-n-db", "l-n-edge", "l-n-bridge"}, 5},
		{"agent host", "boss", "?host=agent-1", http.StatusOK, "agent-1",
			[]string{"a-c-web", "a-c-db", "a-c-solo"}, []string{"a-n-web", "a-n-db", "a-n-edge", "a-n-bridge"}, 5},
		{"host scope sees its host", "tagged", "?host=agent-1", http.StatusOK, "agent-1",
			[]string{"a-c-web", "a-c-db", "a-c-solo"}, []string{"a-n-web", "a-n-db", "a-n-edge", "a-n-bridge"}, 5},
		{"host scope is refused elsewhere", "tagged", "", http.StatusForbidden, "", nil, nil, 0},
		{"stack scope sees its stack and the shared network", "webby", "?host=agent-1", http.StatusOK, "agent-1",
			[]string{"a-c-web"}, []string{"a-n-web", "a-n-edge"}, 2},
		{"stack scope is refused on another host", "webby", "", http.StatusForbidden, "", nil, nil, 0},
		{"all hosts", "boss", "?host=all", http.StatusBadRequest, "", nil, nil, 0},
		{"cluster target", "boss", "?cluster=c1", http.StatusBadRequest, "", nil, nil, 0},
		{"old agent", "boss", "?host=agent-old", http.StatusServiceUnavailable, "", nil, nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := e.do(http.MethodGet, "/networks/topology"+tt.query, e.login(tt.user).AccessToken, nil)
			if rr.Code != tt.status {
				t.Fatalf("status %d, want %d: %s", rr.Code, tt.status, rr.Body)
			}
			if tt.status == http.StatusServiceUnavailable {
				var body map[string]string
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["code"] != "topology_agent_outdated" {
					t.Fatalf("old agent body %s", rr.Body)
				}
			}
			if tt.status != http.StatusOK {
				return
			}
			var got Topology
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Target != (TopoTarget{Kind: "host", ID: tt.target, Name: tt.target + "-name"}) {
				t.Errorf("target = %+v", got.Target)
			}
			var cts, nets []string
			for _, c := range got.Containers {
				cts = append(cts, c.ID)
			}
			for _, n := range got.Networks {
				nets = append(nets, n.ID)
			}
			if !slices.Equal(cts, tt.containers) || !slices.Equal(nets, tt.networks) || len(got.Links) != tt.links {
				t.Errorf("containers %v networks %v links %d", cts, nets, len(got.Links))
			}
		})
	}
}
