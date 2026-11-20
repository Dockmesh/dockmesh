package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/swarmread"
)

// ErrAgentNoSwarmProve: the agent predates the node identity proof.
var ErrAgentNoSwarmProve = errors.New("the agent is too old to prove its Swarm node identity; upgrade it")

// ErrProofRefused: the agent answered the proof request with an error.
var ErrProofRefused = errors.New("the agent refused the Swarm proof")

// ErrLocalNoProof: the server's own host is not asked for a proof.
var ErrLocalNoProof = errors.New("the server's own host does not prove its Swarm node identity")

// SwarmNodeList is SwarmNodes plus the trust root the manager reports.
func (h *LocalHost) SwarmNodeList(ctx context.Context) (agents.SwarmNodesResp, error) {
	if h.cli == nil || !h.cli.Connected() {
		return agents.SwarmNodesResp{}, ErrNoDocker
	}
	v, err := swarmread.Read(ctx, h.cli.Raw(), agents.FrameReqSwarmNodes, nil)
	if err != nil {
		return agents.SwarmNodesResp{}, err
	}
	out, _ := v.(agents.SwarmNodesResp)
	return out, nil
}

// SwarmNodeList is SwarmNodes plus the trust root the manager reports;
// agents that predate it report none.
func (h *RemoteHost) SwarmNodeList(ctx context.Context) (agents.SwarmNodesResp, error) {
	ag, err := h.live()
	if err != nil {
		return agents.SwarmNodesResp{}, err
	}
	if !ag.Hello.Has(agents.CapSwarmNodes) {
		return agents.SwarmNodesResp{}, ErrAgentNoSwarmNodes
	}
	data, err := h.request(ctx, agents.FrameReqSwarmNodes, nil)
	if err != nil {
		return agents.SwarmNodesResp{}, agentSwarmError(err)
	}
	var out agents.SwarmNodesResp
	if err := json.Unmarshal(data, &out); err != nil {
		return agents.SwarmNodesResp{}, fmt.Errorf("decode swarm nodes: %w", err)
	}
	out.Nodes = agents.SanitizeSwarmNodes(out.Nodes)
	if len(out.TrustRoot) > agents.MaxTrustRootPEM {
		out.TrustRoot = ""
	}
	return out, nil
}

// SwarmProve asks the agent to sign req with its Swarm node key.
func (h *RemoteHost) SwarmProve(ctx context.Context, req agents.SwarmProveReq) (agents.SwarmProveResp, error) {
	ag, err := h.live()
	if err != nil {
		return agents.SwarmProveResp{}, err
	}
	if !ag.Hello.Has(agents.CapSwarmProve) {
		return agents.SwarmProveResp{}, ErrAgentNoSwarmProve
	}
	data, err := h.request(ctx, agents.FrameReqSwarmProve, req)
	if err != nil {
		if msg, ok := strings.CutPrefix(err.Error(), "agent: "); ok {
			return agents.SwarmProveResp{}, fmt.Errorf("%w: %s", ErrProofRefused, msg)
		}
		return agents.SwarmProveResp{}, err
	}
	var out agents.SwarmProveResp
	if err := json.Unmarshal(data, &out); err != nil {
		return agents.SwarmProveResp{}, fmt.Errorf("%w: undecodable answer", ErrProofRefused)
	}
	p, ok := agents.SanitizeProof(out)
	if !ok {
		return agents.SwarmProveResp{}, fmt.Errorf("%w: answer too large or empty", ErrProofRefused)
	}
	return p, nil
}

// SwarmNodeList lists the Swarm's nodes and trust root through host id.
func (r *Registry) SwarmNodeList(ctx context.Context, id string) (agents.SwarmNodesResp, error) {
	h, err := r.Pick(id)
	if err != nil {
		return agents.SwarmNodesResp{}, err
	}
	l, ok := h.(interface {
		SwarmNodeList(context.Context) (agents.SwarmNodesResp, error)
	})
	if !ok {
		return agents.SwarmNodesResp{}, ErrUnknownHost
	}
	return l.SwarmNodeList(ctx)
}

// SwarmProve asks agent host id for a node identity proof.
func (r *Registry) SwarmProve(ctx context.Context, id string, req agents.SwarmProveReq) (agents.SwarmProveResp, error) {
	h, err := r.Pick(id)
	if err != nil {
		return agents.SwarmProveResp{}, err
	}
	p, ok := h.(interface {
		SwarmProve(context.Context, agents.SwarmProveReq) (agents.SwarmProveResp, error)
	})
	if !ok {
		return agents.SwarmProveResp{}, ErrLocalNoProof
	}
	return p.SwarmProve(ctx, req)
}

// JoinedAt is when agent id's current session started, or zero when it is
// not connected.
func (r *Registry) JoinedAt(id string) time.Time {
	if r.agents == nil {
		return time.Time{}
	}
	if live := r.agents.GetConnected(id); live != nil {
		return live.JoinedAt
	}
	return time.Time{}
}

// RemoteIP is the peer address of agent id's current session, or "".
func (r *Registry) RemoteIP(id string) string {
	if r.agents == nil {
		return ""
	}
	if live := r.agents.GetConnected(id); live != nil {
		return live.RemoteIP
	}
	return ""
}
