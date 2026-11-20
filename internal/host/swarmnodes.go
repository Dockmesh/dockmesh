package host

import (
	"context"
	"errors"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// ErrAgentNoSwarmNodes: the agent predates the Swarm node list.
var ErrAgentNoSwarmNodes = errors.New("the agent is too old to list Swarm nodes; upgrade it")

// SwarmNodes lists the Swarm's nodes as the local daemon sees them; it
// fails unless the daemon is a manager.
func (h *LocalHost) SwarmNodes(ctx context.Context) ([]agents.SwarmNode, error) {
	l, err := h.SwarmNodeList(ctx)
	return l.Nodes, err
}

// SwarmNodes asks the agent's daemon for the Swarm's nodes; it fails
// unless that daemon is a manager.
func (h *RemoteHost) SwarmNodes(ctx context.Context) ([]agents.SwarmNode, error) {
	l, err := h.SwarmNodeList(ctx)
	return l.Nodes, err
}

// SwarmNodes lists the Swarm's nodes through host id.
func (r *Registry) SwarmNodes(ctx context.Context, id string) ([]agents.SwarmNode, error) {
	l, err := r.SwarmNodeList(ctx, id)
	return l.Nodes, err
}
