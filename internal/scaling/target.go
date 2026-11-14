package scaling

import (
	"context"
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
// service of a host stack. Everything they need to know about the service
// comes from the target, never from what an earlier scale asked for.
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
