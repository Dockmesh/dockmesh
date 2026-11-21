package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/swarmwrite"
)

// ErrAgentNoSwarmWrite: the agent predates the Swarm write frames.
var ErrAgentNoSwarmWrite = errors.New("the agent is too old to deploy to a Swarm; upgrade it")

// ErrSwarmWriteUnknown wraps a write frame that got no answer: the manager
// may have applied all of it, part of it or nothing.
var ErrSwarmWriteUnknown = errors.New("the manager did not answer; the outcome of the write is unknown")

type swarmWriter interface {
	swarmDeploy(ctx context.Context, req agents.SwarmDeployReq) (agents.SwarmDeployResp, error)
	swarmRemove(ctx context.Context, req agents.SwarmRemoveReq) (agents.SwarmRemoveResp, error)
}

func (r *Registry) writer(id string) (swarmWriter, error) {
	h, err := r.Pick(id)
	if err != nil {
		return nil, err
	}
	sw, ok := h.(swarmWriter)
	if !ok {
		return nil, ErrUnknownHost
	}
	return sw, nil
}

// SwarmDeploy sends a stack deploy to manager host id. The request is
// validated here too, so a malformed one never leaves the server.
func (r *Registry) SwarmDeploy(ctx context.Context, id string, req agents.SwarmDeployReq) (agents.SwarmDeployResp, error) {
	if err := req.Validate(); err != nil {
		return agents.SwarmDeployResp{}, err
	}
	w, err := r.writer(id)
	if err != nil {
		return agents.SwarmDeployResp{}, err
	}
	return w.swarmDeploy(ctx, req)
}

// SwarmRemove sends a stack remove to manager host id.
func (r *Registry) SwarmRemove(ctx context.Context, id string, req agents.SwarmRemoveReq) (agents.SwarmRemoveResp, error) {
	if err := req.Validate(); err != nil {
		return agents.SwarmRemoveResp{}, err
	}
	w, err := r.writer(id)
	if err != nil {
		return agents.SwarmRemoveResp{}, err
	}
	return w.swarmRemove(ctx, req)
}

// RegistryAuthFor maps each image with credentials for host id to its
// X-Registry-Auth blob.
func (r *Registry) RegistryAuthFor(ctx context.Context, id string, images []string) (map[string]string, error) {
	if r.auth == nil {
		return map[string]string{}, nil
	}
	return r.auth.AuthForImages(ctx, id, images)
}

func (h *RemoteHost) swarmWrite(ctx context.Context, frameType string, payload, out any) error {
	ag, err := h.live()
	if err != nil {
		return err
	}
	if !ag.Hello.Has(agents.CapSwarmWrite) {
		return ErrAgentNoSwarmWrite
	}
	data, err := h.request(ctx, frameType, payload)
	if err != nil {
		return agentSwarmWriteError(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: decode %s: %s", ErrSwarmWriteUnknown, frameType, err.Error())
	}
	return nil
}

// agentSwarmWriteError tells an error the agent answered from no answer
// at all. The agent answers with an error only before its first write.
func agentSwarmWriteError(err error) error {
	msg, answered := strings.CutPrefix(err.Error(), "agent: ")
	switch {
	case !answered:
		return fmt.Errorf("%w: %s", ErrSwarmWriteUnknown, err.Error())
	case msg == agents.ErrNotSwarmManager.Error():
		return agents.ErrNotSwarmManager
	case strings.HasPrefix(msg, agents.ErrSwarmWriteRefused.Error()):
		return fmt.Errorf("%w%s", agents.ErrSwarmWriteRefused, strings.TrimPrefix(msg, agents.ErrSwarmWriteRefused.Error()))
	}
	return errors.New(msg)
}

func (h *RemoteHost) swarmDeploy(ctx context.Context, req agents.SwarmDeployReq) (agents.SwarmDeployResp, error) {
	var out agents.SwarmDeployResp
	err := h.swarmWrite(ctx, agents.FrameReqSwarmStackDeploy, req, &out)
	return out, err
}

func (h *RemoteHost) swarmRemove(ctx context.Context, req agents.SwarmRemoveReq) (agents.SwarmRemoveResp, error) {
	var out agents.SwarmRemoveResp
	err := h.swarmWrite(ctx, agents.FrameReqSwarmStackRemove, req, &out)
	return out, err
}

func (h *LocalHost) swarmDeploy(ctx context.Context, req agents.SwarmDeployReq) (agents.SwarmDeployResp, error) {
	if h.cli == nil || !h.cli.Connected() {
		return agents.SwarmDeployResp{}, ErrNoDocker
	}
	return swarmwrite.Deploy(ctx, h.cli.Raw(), req)
}

func (h *LocalHost) swarmRemove(ctx context.Context, req agents.SwarmRemoveReq) (agents.SwarmRemoveResp, error) {
	if h.cli == nil || !h.cli.Connected() {
		return agents.SwarmRemoveResp{}, ErrNoDocker
	}
	return swarmwrite.Remove(ctx, h.cli.Raw(), req)
}

// ErrAgentNoSwarmAdopt: the agent predates the Swarm adopt frames.
var ErrAgentNoSwarmAdopt = errors.New("the agent is too old to adopt a Swarm stack; upgrade it")

type swarmAdopter interface {
	swarmExport(ctx context.Context, req agents.SwarmExportReq) (agents.SwarmExportResp, error)
	swarmAdopt(ctx context.Context, req agents.SwarmAdoptReq) (agents.SwarmAdoptResp, error)
}

func (r *Registry) adopter(id string) (swarmAdopter, error) {
	h, err := r.Pick(id)
	if err != nil {
		return nil, err
	}
	sa, ok := h.(swarmAdopter)
	if !ok {
		return nil, ErrUnknownHost
	}
	return sa, nil
}

// SwarmExport reads the full specs of a stack's services through manager
// host id.
func (r *Registry) SwarmExport(ctx context.Context, id string, req agents.SwarmExportReq) (agents.SwarmExportResp, error) {
	if err := req.Validate(); err != nil {
		return agents.SwarmExportResp{}, err
	}
	a, err := r.adopter(id)
	if err != nil {
		return agents.SwarmExportResp{}, err
	}
	return a.swarmExport(ctx, req)
}

// SwarmAdopt labels a stack's services as Dockmesh's through manager host
// id.
func (r *Registry) SwarmAdopt(ctx context.Context, id string, req agents.SwarmAdoptReq) (agents.SwarmAdoptResp, error) {
	if err := req.Validate(); err != nil {
		return agents.SwarmAdoptResp{}, err
	}
	a, err := r.adopter(id)
	if err != nil {
		return agents.SwarmAdoptResp{}, err
	}
	return a.swarmAdopt(ctx, req)
}

func (h *RemoteHost) swarmExport(ctx context.Context, req agents.SwarmExportReq) (agents.SwarmExportResp, error) {
	var out agents.SwarmExportResp
	ag, err := h.live()
	if err != nil {
		return out, err
	}
	if !ag.Hello.Has(agents.CapSwarmAdopt) {
		return out, ErrAgentNoSwarmAdopt
	}
	data, err := h.request(ctx, agents.FrameReqSwarmStackExport, req)
	if err != nil {
		return out, agentSwarmWriteError(err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("decode %s: %w", agents.FrameReqSwarmStackExport, err)
	}
	return out, nil
}

func (h *RemoteHost) swarmAdopt(ctx context.Context, req agents.SwarmAdoptReq) (agents.SwarmAdoptResp, error) {
	var out agents.SwarmAdoptResp
	ag, err := h.live()
	if err != nil {
		return out, err
	}
	if !ag.Hello.Has(agents.CapSwarmAdopt) {
		return out, ErrAgentNoSwarmAdopt
	}
	err = h.swarmWrite(ctx, agents.FrameReqSwarmStackAdopt, req, &out)
	return out, err
}

func (h *LocalHost) swarmExport(ctx context.Context, req agents.SwarmExportReq) (agents.SwarmExportResp, error) {
	if h.cli == nil || !h.cli.Connected() {
		return agents.SwarmExportResp{}, ErrNoDocker
	}
	return swarmwrite.Export(ctx, h.cli.Raw(), req)
}

func (h *LocalHost) swarmAdopt(ctx context.Context, req agents.SwarmAdoptReq) (agents.SwarmAdoptResp, error) {
	if h.cli == nil || !h.cli.Connected() {
		return agents.SwarmAdoptResp{}, ErrNoDocker
	}
	return swarmwrite.Adopt(ctx, h.cli.Raw(), req)
}
