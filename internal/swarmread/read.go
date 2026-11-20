// Package swarmread answers the Swarm read frames from a local daemon,
// for the agent and for the server's own host.
package swarmread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// Timeout is below the server's request timeout, so a slow
// manager answers with an error instead of the server giving up first.
const Timeout = 20 * time.Second

var errInvalidID = errors.New("invalid service id")

var objectRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// ValidRef reports whether s is a service ID or name dockerd accepts.
func ValidRef(s string) bool { return objectRef.MatchString(s) }

// API is the part of the Docker client the Swarm frames use.
type API interface {
	Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error)
	NodeList(ctx context.Context, options client.NodeListOptions) (client.NodeListResult, error)
	ServiceList(ctx context.Context, options client.ServiceListOptions) (client.ServiceListResult, error)
	ServiceInspect(ctx context.Context, serviceID string, options client.ServiceInspectOptions) (client.ServiceInspectResult, error)
	TaskList(ctx context.Context, options client.TaskListOptions) (client.TaskListResult, error)
	NetworkList(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error)
}

// Read answers a Swarm read frame. Every one is refused unless the local
// daemon is a manager of an active Swarm; the answers are built from an
// allow-list of fields, so secret and config data and env values never
// leave the host.
func Read(ctx context.Context, cli API, frameType string, payload json.RawMessage) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if err := requireManager(ctx, cli); err != nil {
		return nil, err
	}
	switch frameType {
	case agents.FrameReqSwarmNodes:
		res, err := cli.NodeList(ctx, client.NodeListOptions{})
		if err != nil {
			return nil, err
		}
		return agents.SwarmNodesResp{Nodes: agents.SwarmNodesFrom(res.Items)}, nil

	case agents.FrameReqSwarmServices:
		res, err := cli.ServiceList(ctx, client.ServiceListOptions{Status: true})
		if err != nil {
			return nil, err
		}
		return agents.SwarmServicesFrom(res.Items), nil

	case agents.FrameReqSwarmServiceInspect:
		var req agents.ResourceIDReq
		if err := json.Unmarshal(payload, &req); err != nil || !ValidRef(req.ID) {
			return nil, errInvalidID
		}
		res, err := cli.ServiceInspect(ctx, req.ID, client.ServiceInspectOptions{})
		if cerrdefs.IsNotFound(err) {
			return nil, agents.ErrSwarmServiceNotFound
		}
		if err != nil {
			return nil, err
		}
		svc := res.Service
		// Inspect carries no task counts; only a list with Status set does.
		// The id filter matches prefixes, hence the equality check.
		list, err := cli.ServiceList(ctx, client.ServiceListOptions{Status: true, Filters: client.Filters{}.Add("id", svc.ID)})
		if err != nil {
			return nil, err
		}
		for _, s := range list.Items {
			if s.ID == svc.ID {
				svc.ServiceStatus = s.ServiceStatus
			}
		}
		return agents.SwarmServiceDetailFrom(svc), nil

	case agents.FrameReqSwarmTasks:
		var req agents.SwarmTasksReq
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, fmt.Errorf("decode %s: %w", frameType, err)
			}
		}
		opts := client.TaskListOptions{}
		if req.ServiceID != "" {
			if !ValidRef(req.ServiceID) {
				return nil, errInvalidID
			}
			opts.Filters = client.Filters{}.Add("service", req.ServiceID)
		}
		res, err := cli.TaskList(ctx, opts)
		if err != nil {
			return nil, err
		}
		return agents.SwarmTasksFrom(res.Items), nil

	case agents.FrameReqSwarmNetworks:
		res, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: client.Filters{}.Add("scope", "swarm")})
		if err != nil {
			return nil, err
		}
		return agents.SwarmNetworksFrom(res.Items), nil
	}
	return nil, fmt.Errorf("unknown swarm frame %q", frameType)
}

func requireManager(ctx context.Context, cli API) error {
	res, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("read swarm state: %w", err)
	}
	sw := res.Info.Swarm
	if sw.LocalNodeState != swarm.LocalNodeStateActive || !sw.ControlAvailable {
		return agents.ErrNotSwarmManager
	}
	return nil
}
