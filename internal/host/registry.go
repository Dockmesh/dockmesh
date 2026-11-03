package host

import (
	"context"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/docker"
)

// Registry exposes hosts to handlers. The local docker daemon is always
// reachable as id "local"; agents are looked up live via the agents
// service so that disconnect/reconnect is reflected immediately.
type Registry struct {
	local  *LocalHost
	agents *agents.Service

	afterDeploy DeployHook
	auth        RegistryAuth

	fixed []Host
}

// RegistryAuth maps the images a request pulls on a host to their
// registry credentials.
type RegistryAuth interface {
	AuthForImages(ctx context.Context, hostID string, images []string) (map[string]string, error)
}

// SetRegistryAuth makes agent deploys, scales and scale checks carry the
// credentials for their images.
func (r *Registry) SetRegistryAuth(a RegistryAuth) { r.auth = a }

func NewRegistry(dockerCli *docker.Client, agentSvc *agents.Service) *Registry {
	return &Registry{
		local:  NewLocal(dockerCli),
		agents: agentSvc,
	}
}

// NewFixedRegistry serves exactly the given hosts instead of the local
// daemon and the live agents, so tests can see which host a request
// reached. A host with ID "local" answers for an empty id.
func NewFixedRegistry(hosts ...Host) *Registry {
	return &Registry{fixed: hosts}
}

// Local returns the shared LocalHost so callers (e.g. main.go's
// git-source auto-deploy) can reuse the same instance that
// handlers.pickHost hands out, which keeps the auth resolver and any
// other wiring consistent across deploy paths.
func (r *Registry) Local() *LocalHost { return r.local }

// Pick resolves a host id to its Host implementation. Empty id or "local"
// returns the local docker. Anything else is looked up against the
// agents service.
func (r *Registry) Pick(id string) (Host, error) {
	if r.fixed != nil {
		if id == "" {
			id = "local"
		}
		for _, h := range r.fixed {
			if h.ID() == id {
				return h, nil
			}
		}
		return nil, ErrUnknownHost
	}
	if id == "" || id == "local" {
		return r.local, nil
	}
	if r.agents == nil {
		return nil, ErrUnknownHost
	}
	live := r.agents.GetConnected(id)
	if live == nil {
		return nil, ErrAgentOffline
	}
	rh := NewRemote(live.ID, live.Name, r.agents)
	rh.afterDeploy = r.afterDeploy
	rh.auth = r.auth
	return rh, nil
}

// List returns all known hosts (local + every registered agent, online or
// not) as compact metadata for the host switcher.
func (r *Registry) List(ctx context.Context) ([]Info, error) {
	out := []Info{
		{ID: "local", Name: "Local", Kind: "local", Status: "online"},
	}
	if r.agents == nil {
		return out, nil
	}
	list, err := r.agents.List(ctx)
	if err != nil {
		return out, err
	}
	for _, a := range list {
		out = append(out, Info{
			ID:     a.ID,
			Name:   a.Name,
			Kind:   "agent",
			Status: a.Status,
		})
	}
	return out, nil
}
