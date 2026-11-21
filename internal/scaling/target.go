package scaling

import (
	"context"
	"errors"
	"strings"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacklock"
)

// Replica is one running container of a scaled service. Name is what its
// metrics are stored under.
type Replica struct {
	Name        string
	ContainerID string
}

// Target is one service that manual scale and the auto-scaler change: a
// service of a host stack, or of a stack on a Swarm cluster.
type Target interface {
	// MetricsHost is the host id the replicas' metrics are stored under.
	MetricsHost() string
	// LockKey is the stack lock the scale takes.
	LockKey() stacklock.Key
	// Replicas returns the running replicas, found by their labels.
	Replicas(ctx context.Context) ([]Replica, error)
	// Scale sets the replica count from the given stack files.
	Scale(ctx context.Context, composeYAML, envContent string, replicas int) (*compose.ScaleResult, error)
}

// HostTarget is a service of a stack on one host.
type HostTarget struct {
	host    host.Host
	stack   string
	service string
}

func NewHostTarget(h host.Host, stack, service string) *HostTarget {
	return &HostTarget{host: h, stack: stack, service: service}
}

func (t *HostTarget) MetricsHost() string { return t.host.ID() }

func (t *HostTarget) LockKey() stacklock.Key { return stacklock.HostKey(t.host.ID(), t.stack) }

func (t *HostTarget) Replicas(ctx context.Context) ([]Replica, error) {
	list, err := t.host.ListContainers(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []Replica
	for _, c := range list {
		if c.Labels[compose.LabelProject] != t.stack || c.Labels[compose.LabelService] != t.service {
			continue
		}
		switch c.State {
		case "running", "restarting", "paused":
		default:
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, Replica{Name: name, ContainerID: c.ID})
	}
	return out, nil
}

func (t *HostTarget) Scale(ctx context.Context, composeYAML, envContent string, replicas int) (*compose.ScaleResult, error) {
	return t.host.ScaleService(ctx, t.stack, composeYAML, envContent, t.service, replicas)
}

// ErrNoClusterMetrics: replica metrics of a Swarm service are not read, so
// the auto-scaler has no rule on a cluster target.
var ErrNoClusterMetrics = errors.New("the auto-scaler does not read the metrics of Swarm services")

// ClusterTarget is a service of a stack on a Swarm cluster. Apply sets the
// replica count through a manager and returns the count before.
type ClusterTarget struct {
	ClusterID, Stack, Service string
	Apply                     func(ctx context.Context, replicas int) (previous int, err error)
}

func (t *ClusterTarget) MetricsHost() string { return "" }

func (t *ClusterTarget) LockKey() stacklock.Key { return stacklock.ClusterKey(t.ClusterID, t.Stack) }

func (t *ClusterTarget) Replicas(context.Context) ([]Replica, error) { return nil, ErrNoClusterMetrics }

// Scale sets the count on the cluster; Swarm starts or stops the tasks.
// The stack files are not used: the deploy that wrote the service did.
func (t *ClusterTarget) Scale(ctx context.Context, _, _ string, replicas int) (*compose.ScaleResult, error) {
	prev, err := t.Apply(ctx, replicas)
	if err != nil {
		return nil, err
	}
	res := &compose.ScaleResult{Service: t.Service, Previous: prev, Current: replicas}
	if replicas > prev {
		res.Created = replicas - prev
	} else {
		res.Removed = prev - replicas
	}
	return res, nil
}
