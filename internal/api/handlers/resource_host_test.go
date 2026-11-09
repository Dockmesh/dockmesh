package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/stacks/adopt"
)

// recordingHost logs every resource mutation it receives. Methods the
// test doesn't route to fall through to the nil embedded Host and panic.
type recordingHost struct {
	host.Host
	id string

	mu    sync.Mutex
	calls []string
}

func (f *recordingHost) ID() string   { return f.id }
func (f *recordingHost) Name() string { return f.id }

func (f *recordingHost) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *recordingHost) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *recordingHost) CreateVolume(_ context.Context, name, _ string, _ map[string]string) (volume.Volume, error) {
	f.record("volume.create " + name)
	return volume.Volume{Name: name}, nil
}

func (f *recordingHost) RemoveVolume(_ context.Context, name string, _ bool) error {
	f.record("volume.remove " + name)
	return nil
}

func (f *recordingHost) PruneVolumes(context.Context) (volume.PruneReport, error) {
	f.record("volume.prune")
	return volume.PruneReport{}, nil
}

func (f *recordingHost) RemoveImage(_ context.Context, id string, _ bool) ([]image.DeleteResponse, error) {
	f.record("image.remove " + id)
	return nil, nil
}

func (f *recordingHost) PruneImages(context.Context) (image.PruneReport, error) {
	f.record("image.prune")
	return image.PruneReport{}, nil
}

func (f *recordingHost) CreateNetwork(_ context.Context, name, _ string, _ map[string]string) (network.CreateResponse, error) {
	f.record("network.create " + name)
	return network.CreateResponse{ID: name}, nil
}

func (f *recordingHost) RemoveNetwork(_ context.Context, id string) error {
	f.record("network.remove " + id)
	return nil
}

func (f *recordingHost) PruneNetworks(context.Context) (network.PruneReport, error) {
	f.record("network.prune")
	return network.PruneReport{}, nil
}

// Every destructive or creating resource call acts on the host it names
// and only there; a caller scoped to the agent can't reach the server.
func TestResourceCallsStayOnTheirHost(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO agents (id, name, status) VALUES ('agent-1', 'agent-1', 'online')`); err != nil {
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

	local := &recordingHost{id: "local"}
	agent := &recordingHost{id: "agent-1"}
	e.h.Hosts = host.NewFixedRegistry(local, agent)

	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermVolumesCreate)).Post("/volumes", e.h.CreateVolume)
		r.With(middleware.RequirePerm(rbac.PermVolumesDelete)).Delete("/volumes/{name}", e.h.RemoveVolume)
		r.With(middleware.RequirePerm(rbac.PermVolumesDelete)).Post("/volumes/prune", e.h.PruneVolumes)
		r.With(middleware.RequirePerm(rbac.PermImagesDelete)).Delete("/images/{id}", e.h.RemoveImage)
		r.With(middleware.RequirePerm(rbac.PermImagesDelete)).Post("/images/prune", e.h.PruneImages)
		r.With(middleware.RequirePerm(rbac.PermNetworksCreate)).Post("/networks", e.h.CreateNetwork)
		r.With(middleware.RequirePerm(rbac.PermNetworksDelete)).Delete("/networks/{id}", e.h.RemoveNetwork)
		r.With(middleware.RequirePerm(rbac.PermNetworksDelete)).Post("/networks/prune", e.h.PruneNetworks)
	})
	e.user("boss", "admin")
	scoped := e.user("edge", "host-admin")
	e.scopeUser(scoped.ID, "prod")
	boss := e.login("boss").AccessToken
	edge := e.login("edge").AccessToken

	calls := []struct {
		method, path string
		body         any
		want         string
	}{
		{http.MethodPost, "/volumes", map[string]string{"name": "data"}, "volume.create data"},
		{http.MethodDelete, "/volumes/data", nil, "volume.remove data"},
		{http.MethodPost, "/volumes/prune", nil, "volume.prune"},
		{http.MethodDelete, "/images/sha256:abc", nil, "image.remove sha256:abc"},
		{http.MethodPost, "/images/prune", nil, "image.prune"},
		{http.MethodPost, "/networks", map[string]string{"name": "web"}, "network.create web"},
		{http.MethodDelete, "/networks/web", nil, "network.remove web"},
		{http.MethodPost, "/networks/prune", nil, "network.prune"},
	}
	reset := func() {
		for _, hh := range []*recordingHost{local, agent} {
			hh.mu.Lock()
			hh.calls = nil
			hh.mu.Unlock()
		}
	}

	targets := []struct {
		name, query     string
		reached, spared *recordingHost
	}{
		{"agent", "agent-1", agent, local},
		{"server", "local", local, agent},
		{"no host means the server", "", local, agent},
	}
	for _, tg := range targets {
		for _, c := range calls {
			t.Run(tg.name+"/"+c.want, func(t *testing.T) {
				reset()
				rr := e.do(c.method, hostQuery(c.path, tg.query), boss, c.body)
				if rr.Code >= 300 {
					t.Fatalf("status %d: %s", rr.Code, rr.Body)
				}
				if got := tg.reached.took(); !slices.Equal(got, []string{c.want}) {
					t.Fatalf("%s got %v, want [%s]", tg.reached.id, got, c.want)
				}
				if got := tg.spared.took(); len(got) != 0 {
					t.Fatalf("%s was touched: %v", tg.spared.id, got)
				}
			})
		}
	}

	for _, c := range calls {
		t.Run("scoped to agent/"+c.want, func(t *testing.T) {
			reset()
			if rr := e.do(c.method, hostQuery(c.path, "local"), edge, c.body); rr.Code != http.StatusForbidden {
				t.Fatalf("server host: status %d, want 403: %s", rr.Code, rr.Body)
			}
			if got := local.took(); len(got) != 0 {
				t.Fatalf("server host was touched by an agent-scoped caller: %v", got)
			}
			rr := e.do(c.method, hostQuery(c.path, "agent-1"), edge, c.body)
			if rr.Code >= 300 {
				t.Fatalf("own host: status %d: %s", rr.Code, rr.Body)
			}
			if got := agent.took(); !slices.Equal(got, []string{c.want}) {
				t.Fatalf("agent got %v", got)
			}
		})
	}

	t.Run("unknown host", func(t *testing.T) {
		reset()
		rr := e.do(http.MethodDelete, "/volumes/data?host=agent-2", boss, nil)
		if rr.Code < 400 || !strings.Contains(rr.Body.String(), "unknown") {
			t.Fatalf("status %d: %s", rr.Code, rr.Body)
		}
		if len(local.took())+len(agent.took()) != 0 {
			t.Fatal("an unknown host fell back to a real one")
		}
	})
}

type composeHost struct {
	recordingHost
	projects []string
	err      error
}

func (f *composeHost) ListContainers(context.Context, bool) ([]container.Summary, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]container.Summary, 0, len(f.projects))
	for _, p := range f.projects {
		out = append(out, container.Summary{
			ID:     f.id + "-" + p,
			Image:  "nginx",
			Names:  []string{"/" + p + "-web-1"},
			Labels: map[string]string{"com.docker.compose.project": p, "com.docker.compose.service": "web"},
		})
	}
	return out, nil
}

// Discovery under All hosts asks every host the caller may see and
// merges the answers; a host that doesn't answer leaves the rest intact.
func TestDiscoverStacksAllHosts(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO agents (id, name, status) VALUES ('agent-1', 'agent-1', 'online')`); err != nil {
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
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.h.Stacks = mgr
	e.h.Hosts = host.NewFixedRegistry(
		&composeHost{recordingHost: recordingHost{id: "local"}, projects: []string{"blog"}},
		&composeHost{recordingHost: recordingHost{id: "agent-1"}, projects: []string{"blog", "shop"}},
		&composeHost{recordingHost: recordingHost{id: "agent-2"}, err: host.ErrAgentOffline},
	)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksAdopt)).Get("/stacks/discovered", e.h.DiscoverStacks)
	})
	e.user("boss", "admin")
	scoped := e.user("edge", "host-admin")
	e.scopeUser(scoped.ID, "prod")

	found := func(user, query string) []string {
		t.Helper()
		rr := e.do(http.MethodGet, "/stacks/discovered"+query, e.login(user).AccessToken, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d: %s", user, query, rr.Code, rr.Body)
		}
		var list []adopt.DiscoveredStack
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(list))
		for _, d := range list {
			out = append(out, d.HostID+"/"+d.ProjectName)
		}
		return out
	}
	cases := []struct {
		user, query string
		want        []string
	}{
		{"boss", "?host=all", []string{"agent-1/blog", "local/blog", "agent-1/shop"}},
		{"boss", "?host=agent-1", []string{"agent-1/blog", "agent-1/shop"}},
		{"boss", "", []string{"local/blog"}},
		{"edge", "?host=all", []string{"agent-1/blog", "agent-1/shop"}},
	}
	for _, c := range cases {
		if got := found(c.user, c.query); !slices.Equal(got, c.want) {
			t.Errorf("%s %s: got %v, want %v", c.user, c.query, got, c.want)
		}
	}
	rr := e.do(http.MethodGet, "/stacks/discovered?host=agent-2", e.login("boss").AccessToken, nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("offline host: status %d, want 503", rr.Code)
	}
}
