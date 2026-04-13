package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dockmesh/dockmesh/internal/agents"
	dtypes "github.com/docker/docker/api/types"
)

// RemoteHost proxies operations to a connected agent over its WebSocket
// using the request/response protocol in internal/agents.
type RemoteHost struct {
	id    string
	name  string
	agent *agents.ConnectedAgent
}

func NewRemote(id, name string, ag *agents.ConnectedAgent) *RemoteHost {
	return &RemoteHost{id: id, name: name, agent: ag}
}

func (h *RemoteHost) ID() string   { return h.id }
func (h *RemoteHost) Name() string { return h.name }

func (h *RemoteHost) request(ctx context.Context, frameType string, payload any) (json.RawMessage, error) {
	if h.agent == nil {
		return nil, ErrAgentOffline
	}
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	env, err := h.agent.Request(ctx, agents.Frame{Type: frameType, Payload: raw})
	if err != nil {
		return nil, err
	}
	return env.Data, nil
}

func (h *RemoteHost) ListContainers(ctx context.Context, all bool) ([]dtypes.Container, error) {
	data, err := h.request(ctx, agents.FrameReqContainerList, agents.ContainerListReq{All: all})
	if err != nil {
		return nil, err
	}
	var out []dtypes.Container
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	if out == nil {
		out = []dtypes.Container{}
	}
	return out, nil
}

func (h *RemoteHost) InspectContainer(ctx context.Context, id string) (dtypes.ContainerJSON, error) {
	data, err := h.request(ctx, agents.FrameReqContainerInspect, agents.ContainerIDReq{ID: id})
	if err != nil {
		return dtypes.ContainerJSON{}, err
	}
	var out dtypes.ContainerJSON
	if err := json.Unmarshal(data, &out); err != nil {
		return dtypes.ContainerJSON{}, fmt.Errorf("decode inspect: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) ListImages(ctx context.Context, all bool) ([]dtypes.ImageSummary, error) {
	data, err := h.request(ctx, agents.FrameReqImageList, map[string]bool{"all": all})
	if err != nil {
		return nil, err
	}
	var out []dtypes.ImageSummary
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode images: %w", err)
	}
	if out == nil {
		out = []dtypes.ImageSummary{}
	}
	return out, nil
}

func (h *RemoteHost) ListNetworks(ctx context.Context) ([]dtypes.NetworkResource, error) {
	data, err := h.request(ctx, agents.FrameReqNetworkList, nil)
	if err != nil {
		return nil, err
	}
	var out []dtypes.NetworkResource
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode networks: %w", err)
	}
	if out == nil {
		out = []dtypes.NetworkResource{}
	}
	return out, nil
}

func (h *RemoteHost) ListVolumes(ctx context.Context) ([]any, error) {
	data, err := h.request(ctx, agents.FrameReqVolumeList, nil)
	if err != nil {
		return nil, err
	}
	var out []any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode volumes: %w", err)
	}
	if out == nil {
		out = []any{}
	}
	return out, nil
}

// Errors
var (
	ErrAgentOffline = errors.New("agent offline")
	ErrNoDocker     = errors.New("docker daemon unavailable")
	ErrUnknownHost  = errors.New("unknown host")
)
