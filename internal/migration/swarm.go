package migration

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/moby/api/types/container"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/updater"
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

// isSwarmStack reports whether stack runs on h as Swarm tasks.
func isSwarmStack(ctx context.Context, h host.Host, stack string) (bool, error) {
	list, err := h.ListContainers(ctx, true)
	if err != nil {
		return false, err
	}
	return swarmTasksOf(list, stack), nil
}

func swarmTasksOf(list []container.Summary, stack string) bool {
	for _, c := range list {
		if c.Labels[labelStackNamespace] == stack {
			return true
		}
		if c.Labels["com.docker.compose.project"] == stack && updater.SwarmService(c.Labels) != "" {
			return true
		}
	}
	return false
}
