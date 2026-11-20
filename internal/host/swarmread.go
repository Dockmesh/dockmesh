package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/swarmread"
)

// ErrAgentNoSwarmRead: the agent predates the Swarm read frames.
var ErrAgentNoSwarmRead = errors.New("the agent is too old to read Swarm services, tasks and networks; upgrade it")

// swarmRead sends one Swarm read frame and returns the raw answer. An
// agent's "not a manager" or "not found" comes back as the sentinel.
func (h *RemoteHost) swarmRead(ctx context.Context, frameType string, payload any) (json.RawMessage, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	if !ag.Hello.Has(agents.CapSwarmRead) {
		return nil, ErrAgentNoSwarmRead
	}
	data, err := h.request(ctx, frameType, payload)
	if err != nil {
		return nil, agentSwarmError(err)
	}
	return data, nil
}

func agentSwarmError(err error) error {
	for _, s := range []error{agents.ErrNotSwarmManager, agents.ErrSwarmServiceNotFound} {
		if strings.HasSuffix(err.Error(), "agent: "+s.Error()) {
			return s
		}
	}
	return err
}

func (h *LocalHost) swarmRead(ctx context.Context, frameType string, payload any) (json.RawMessage, error) {
	if h.cli == nil || !h.cli.Connected() {
		return nil, ErrNoDocker
	}
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	out, err := swarmread.Read(ctx, h.cli.Raw(), frameType, raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

type swarmReader interface {
	swarmRead(ctx context.Context, frameType string, payload any) (json.RawMessage, error)
}

func (r *Registry) reader(id string) (swarmReader, error) {
	h, err := r.Pick(id)
	if err != nil {
		return nil, err
	}
	sr, ok := h.(swarmReader)
	if !ok {
		return nil, ErrUnknownHost
	}
	return sr, nil
}

func readSwarm(ctx context.Context, sr swarmReader, frameType string, payload, out any) error {
	data, err := sr.swarmRead(ctx, frameType, payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s: %w", frameType, err)
	}
	return nil
}

// SwarmServices lists the Swarm's services through manager host id.
func (r *Registry) SwarmServices(ctx context.Context, id string) (agents.SwarmServicesResp, error) {
	sr, err := r.reader(id)
	if err != nil {
		return agents.SwarmServicesResp{}, err
	}
	return swarmServices(ctx, sr)
}

func swarmServices(ctx context.Context, sr swarmReader) (agents.SwarmServicesResp, error) {
	var out agents.SwarmServicesResp
	if err := readSwarm(ctx, sr, agents.FrameReqSwarmServices, nil, &out); err != nil {
		return agents.SwarmServicesResp{}, err
	}
	return agents.SanitizeSwarmServices(out), nil
}

// SwarmService inspects one service through manager host id.
func (r *Registry) SwarmService(ctx context.Context, id, service string) (agents.SwarmServiceDetail, error) {
	sr, err := r.reader(id)
	if err != nil {
		return agents.SwarmServiceDetail{}, err
	}
	return swarmService(ctx, sr, service)
}

func swarmService(ctx context.Context, sr swarmReader, service string) (agents.SwarmServiceDetail, error) {
	var out agents.SwarmServiceDetail
	if err := readSwarm(ctx, sr, agents.FrameReqSwarmServiceInspect, agents.ResourceIDReq{ID: service}, &out); err != nil {
		return agents.SwarmServiceDetail{}, err
	}
	d := agents.SanitizeSwarmServiceDetail(out)
	if d.ID == "" {
		return agents.SwarmServiceDetail{}, agents.ErrSwarmServiceNotFound
	}
	return d, nil
}

// SwarmTasks lists the Swarm's tasks through manager host id, only those
// of service when it is set.
func (r *Registry) SwarmTasks(ctx context.Context, id, service string) (agents.SwarmTasksResp, error) {
	sr, err := r.reader(id)
	if err != nil {
		return agents.SwarmTasksResp{}, err
	}
	return swarmTasks(ctx, sr, service)
}

func swarmTasks(ctx context.Context, sr swarmReader, service string) (agents.SwarmTasksResp, error) {
	var out agents.SwarmTasksResp
	if err := readSwarm(ctx, sr, agents.FrameReqSwarmTasks, agents.SwarmTasksReq{ServiceID: service}, &out); err != nil {
		return agents.SwarmTasksResp{}, err
	}
	return agents.SanitizeSwarmTasks(out), nil
}

// SwarmNetworks lists the networks with Swarm scope through manager host id.
func (r *Registry) SwarmNetworks(ctx context.Context, id string) (agents.SwarmNetworksResp, error) {
	sr, err := r.reader(id)
	if err != nil {
		return agents.SwarmNetworksResp{}, err
	}
	return swarmNetworks(ctx, sr)
}

func swarmNetworks(ctx context.Context, sr swarmReader) (agents.SwarmNetworksResp, error) {
	var out agents.SwarmNetworksResp
	if err := readSwarm(ctx, sr, agents.FrameReqSwarmNetworks, nil, &out); err != nil {
		return agents.SwarmNetworksResp{}, err
	}
	return agents.SanitizeSwarmNetworks(out), nil
}
