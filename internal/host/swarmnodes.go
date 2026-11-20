package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
