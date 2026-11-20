package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/moby/moby/api/types/system"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// ErrAgentNoSwarmNodes: the agent predates the Swarm node list.
var ErrAgentNoSwarmNodes = errors.New("the agent is too old to list Swarm nodes; upgrade it")

// SwarmNodes lists the Swarm's nodes as the local daemon sees them; it
// fails unless the daemon is a manager.
func (h *LocalHost) SwarmNodes(ctx context.Context) ([]agents.SwarmNode, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	nodes, err := h.cli.SwarmNodes(ctx)
	if err != nil {
		return nil, err
	}
	return agents.SwarmNodesFrom(nodes), nil
}

// SwarmNodes asks the agent's daemon for the Swarm's nodes; it fails
// unless that daemon is a manager.
func (h *RemoteHost) SwarmNodes(ctx context.Context) ([]agents.SwarmNode, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	if !ag.Hello.Has(agents.CapSwarmNodes) {
		return nil, ErrAgentNoSwarmNodes
	}
	data, err := h.request(ctx, agents.FrameReqSwarmNodes, nil)
	if err != nil {
		return nil, err
	}
	var out agents.SwarmNodesResp
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode swarm nodes: %w", err)
	}
	return agents.SanitizeSwarmNodes(out.Nodes), nil
}

// SwarmNodes lists the Swarm's nodes through host id.
func (r *Registry) SwarmNodes(ctx context.Context, id string) ([]agents.SwarmNode, error) {
	h, err := r.Pick(id)
	if err != nil {
		return nil, err
	}
	l, ok := h.(interface {
		SwarmNodes(context.Context) ([]agents.SwarmNode, error)
	})
	if !ok {
		return nil, ErrUnknownHost
	}
	return l.SwarmNodes(ctx)
}

// DaemonInfo asks the agent's daemon now, so a host that joined or left
// a Swarm after the agent connected is seen without a reconnect.
func (h *RemoteHost) DaemonInfo(ctx context.Context) (*agents.DaemonInfo, error) {
	data, err := h.request(ctx, agents.FrameReqDaemonInfo, nil)
	if err != nil {
		return nil, err
	}
	var info system.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("decode daemon info: %w", err)
	}
	return agents.DaemonInfoFrom(info), nil
}

// CurrentDaemonInfo is DaemonInfo asked live from an online agent; an
// offline agent falls back to what its last hello reported.
func (r *Registry) CurrentDaemonInfo(ctx context.Context, id string) (*agents.DaemonInfo, error) {
	if id == "" || id == "local" || r.agents == nil || r.fixed != nil {
		return r.DaemonInfo(ctx, id)
	}
	if live := r.agents.GetConnected(id); live != nil {
		if info, err := NewRemote(live.ID, live.Name, r.agents).DaemonInfo(ctx); err == nil {
			return info, nil
		}
	}
	return r.DaemonInfo(ctx, id)
}
