package migration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/moby/moby/api/types/container"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/swarmtask"
)

// ErrSwarmStack refuses moving a stack whose containers are Swarm tasks.
var ErrSwarmStack = errors.New("Swarm places tasks itself. Use node drain or a placement constraint in the compose file.") //nolint:staticcheck // shown to the user as a sentence

// ErrClusterTarget refuses a migration into a Swarm cluster.
var ErrClusterTarget = errors.New("moving a host stack into a Swarm cluster is not supported; deploy it to the cluster instead")

// Target kinds a migration request may name.
const (
	TargetKindHost    = "host"
	TargetKindCluster = "cluster"
)

// CheckTargetKind refuses every target kind but a host.
func CheckTargetKind(kind string) error {
	switch kind {
	case "", TargetKindHost:
		return nil
	case TargetKindCluster:
		return ErrClusterTarget
	}
	return fmt.Errorf("unknown target_kind %q", kind)
}

// labelStackNamespace is set by `docker stack deploy` on every task
// container and volume of a Swarm stack.
const labelStackNamespace = "com.docker.stack.namespace"

const labelComposeProject = "com.docker.compose.project"

// ErrSwarmUnclear refuses a stack on a Swarm node that has no compose
// container there: its tasks may all run on other nodes, which only a
// manager could list.
var ErrSwarmUnclear = errors.New("the host is a Swarm node and the stack has no containers on it, so it may be a Swarm stack; start it on the host first")

// errSwarmCheck hides the agent's own error, which is logged.
var errSwarmCheck = errors.New("cannot list the host's containers to rule out a Swarm stack")

// swarmScan holds what the Swarm check needs from one host.
type swarmScan struct {
	containers []container.Summary
	swarmNode  bool
	err        error
}

// scanForSwarm reads h's containers and Swarm membership once.
func (s *Service) scanForSwarm(ctx context.Context, h host.Host) swarmScan {
	list, err := h.ListContainers(ctx, true)
	if err != nil {
		slog.Warn("migration swarm check: list containers", "host", h.ID(), "err", err)
		return swarmScan{err: errSwarmCheck}
	}
	scan := swarmScan{containers: list}
	if info, err := s.hosts.DaemonInfo(ctx, h.ID()); err == nil && info != nil && info.Swarm != nil {
		scan.swarmNode = info.Swarm.NodeState != "" && info.Swarm.NodeState != "inactive"
	} else {
		slog.Warn("migration swarm check: the host doesn't report its Swarm state", "host", h.ID(), "err", err)
	}
	return scan
}

// check returns ErrSwarmStack when stack runs as Swarm tasks here,
// ErrSwarmUnclear when that can't be ruled out, and nil for a host stack.
func (sc swarmScan) check(stack string) error {
	if sc.err != nil {
		return sc.err
	}
	composeHere := false
	for _, c := range sc.containers {
		// Dockmesh sets the compose project of what it deploys, so a
		// compose service can't pass itself off as another stack's task.
		if p := c.Labels[labelComposeProject]; p != "" {
			if p != stack {
				continue
			}
			if swarmtask.Service(c.Labels) != "" {
				return ErrSwarmStack
			}
			if c.State == container.StateRunning {
				composeHere = true
			}
			continue
		}
		if c.Labels[labelStackNamespace] == stack && swarmtask.Service(c.Labels) != "" {
			return ErrSwarmStack
		}
	}
	if sc.swarmNode && !composeHere {
		return ErrSwarmUnclear
	}
	return nil
}

// checkSwarm is scanForSwarm(h).check(stack).
func (s *Service) checkSwarm(ctx context.Context, h host.Host, stack string) error {
	return s.scanForSwarm(ctx, h).check(stack)
}
