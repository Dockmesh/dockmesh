package swarmwrite

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// ServiceAction rolls back or resumes the update of one service Dockmesh
// deployed. Rollback is Swarm's own, to the service's previous spec.
// Resume submits the current spec again, which restarts the paused
// update for the tasks it hasn't reached yet; the image stays pinned to
// the digest the update was started with.
func ServiceAction(ctx context.Context, cli API, req agents.SwarmServiceActionReq) (agents.SwarmWriteResult, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if err := req.Validate(); err != nil {
		return agents.SwarmWriteResult{}, err
	}
	if err := requireManager(ctx, cli); err != nil {
		return agents.SwarmWriteResult{}, err
	}
	found, err := servicesNamed(ctx, cli, []string{req.Service})
	if err != nil {
		return agents.SwarmWriteResult{}, err
	}
	cur, ok := found[req.Service]
	switch {
	case !ok || cur.ID != req.ID:
		return agents.SwarmWriteResult{}, fmt.Errorf("%w: service %s is gone or was replaced", agents.ErrSwarmWriteRefused, req.Service)
	case !ownedService(cur, req.Stack):
		return agents.SwarmWriteResult{}, fmt.Errorf("%w: service %s was not deployed by Dockmesh for stack %s", agents.ErrSwarmWriteRefused, req.Service, req.Stack)
	}
	opts := client.ServiceUpdateOptions{Version: cur.Version, Spec: cur.Spec}
	switch req.Action {
	case agents.SwarmServiceRollback:
		if cur.PreviousSpec == nil {
			return agents.SwarmWriteResult{}, fmt.Errorf("%w: service %s has no previous spec to roll back to", agents.ErrSwarmWriteRefused, req.Service)
		}
		if !ownedService(swarm.Service{Spec: *cur.PreviousSpec}, req.Stack) {
			return agents.SwarmWriteResult{}, fmt.Errorf("%w: the previous spec of service %s was not deployed by Dockmesh", agents.ErrSwarmWriteRefused, req.Service)
		}
		// After a rollback the previous spec is the one rolled back from;
		// putting it back is a deploy, not a rollback.
		switch updateState(cur) {
		case swarm.UpdateStateRollbackStarted:
			return agents.SwarmWriteResult{}, fmt.Errorf("%w: service %s is already rolling back", agents.ErrSwarmWriteRefused, req.Service)
		case swarm.UpdateStateRollbackPaused, swarm.UpdateStateRollbackCompleted:
			return agents.SwarmWriteResult{}, fmt.Errorf("%w: service %s was rolled back; deploy to change it again", agents.ErrSwarmWriteRefused, req.Service)
		}
		opts.Rollback = "previous"
	case agents.SwarmServiceResume:
		if s := updateState(cur); s != swarm.UpdateStatePaused && s != swarm.UpdateStateRollbackPaused {
			return agents.SwarmWriteResult{}, fmt.Errorf("%w: the update of service %s is not paused", agents.ErrSwarmWriteRefused, req.Service)
		}
	}
	res, err := cli.ServiceUpdate(ctx, cur.ID, opts)
	if err != nil {
		return failed(ctx, req.Service, cur.ID, err), nil
	}
	return agents.SwarmWriteResult{Name: req.Service, ID: cur.ID, Action: agents.SwarmActionUpdated, Warnings: res.Warnings}, nil
}

func updateState(s swarm.Service) swarm.UpdateState {
	if s.UpdateStatus == nil {
		return ""
	}
	return s.UpdateStatus.State
}
