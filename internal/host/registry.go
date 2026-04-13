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
}

func NewRegistry(dockerCli *docker.Client, agentSvc *agents.Service) *Registry {
	return &Registry{
		local:  NewLocal(dockerCli),
		agents: agentSvc,
	}
}

// Pick resolves a host id to its Host implementation. Empty id or "local"
// returns the local docker. Anything else is looked up against the
// agents service.
func (r *Registry) Pick(id string) (Host, error) {
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
	return NewRemote(live.ID, live.Name, live), nil
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
