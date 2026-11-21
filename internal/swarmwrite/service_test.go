package swarmwrite

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func ownedWith(state swarm.UpdateState, previous bool) swarm.Service {
	s := cliService("1", "shop_web", "shop", 5, map[string]string{agents.LabelSpecHash: hash})
	if state != "" {
		s.UpdateStatus = &swarm.UpdateStatus{State: state}
	}
	if previous {
		prev := s.Spec
		s.PreviousSpec = &prev
	}
	return s
}

func TestServiceAction(t *testing.T) {
	req := func(action string) agents.SwarmServiceActionReq {
		return agents.SwarmServiceActionReq{Stack: "shop", Service: "shop_web", ID: "1", Action: action}
	}
	cases := []struct {
		name     string
		svc      swarm.Service
		req      agents.SwarmServiceActionReq
		rollback string
		refused  bool
	}{
		{"rollback a paused update", ownedWith(swarm.UpdateStatePaused, true), req(agents.SwarmServiceRollback), "previous", false},
		{"rollback a completed update", ownedWith(swarm.UpdateStateCompleted, true), req(agents.SwarmServiceRollback), "previous", false},
		{"rollback without previous spec", ownedWith(swarm.UpdateStatePaused, false), req(agents.SwarmServiceRollback), "", true},
		{"rollback while rolling back", ownedWith(swarm.UpdateStateRollbackStarted, true), req(agents.SwarmServiceRollback), "", true},
		{"rollback after a rollback", ownedWith(swarm.UpdateStateRollbackCompleted, true), req(agents.SwarmServiceRollback), "", true},
		{"rollback a paused rollback", ownedWith(swarm.UpdateStateRollbackPaused, true), req(agents.SwarmServiceRollback), "", true},
		{"resume a paused update", ownedWith(swarm.UpdateStatePaused, true), req(agents.SwarmServiceResume), "", false},
		{"resume a paused rollback", ownedWith(swarm.UpdateStateRollbackPaused, true), req(agents.SwarmServiceResume), "", false},
		{"resume a running update", ownedWith(swarm.UpdateStateUpdating, true), req(agents.SwarmServiceResume), "", true},
		{"resume without update", ownedWith("", true), req(agents.SwarmServiceResume), "", true},
		{"rollback to a spec from outside Dockmesh", adoptedWith(), req(agents.SwarmServiceRollback), "", true},
		{"external service", cliService("1", "shop_web", "shop", 5, nil), req(agents.SwarmServiceRollback), "", true},
		{"replaced service", ownedWith(swarm.UpdateStatePaused, true), agents.SwarmServiceActionReq{Stack: "shop", Service: "shop_web", ID: "9", Action: "rollback"}, "", true},
		{"other stack's name", ownedWith(swarm.UpdateStatePaused, true), agents.SwarmServiceActionReq{Stack: "other", Service: "shop_web", ID: "1", Action: "rollback"}, "", true},
		{"unknown action", ownedWith(swarm.UpdateStatePaused, true), req("scale"), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDaemon{services: []swarm.Service{tc.svc}}
			res, err := ServiceAction(context.Background(), f, tc.req)
			if tc.refused {
				if !errors.Is(err, agents.ErrSwarmWriteRefused) || len(f.updates) != 0 {
					t.Fatalf("want a refusal without a write, got %v, %d writes", err, len(f.updates))
				}
				return
			}
			if err != nil || res.Action != agents.SwarmActionUpdated || len(f.updates) != 1 {
				t.Fatalf("res %+v err %v writes %d", res, err, len(f.updates))
			}
			u := f.updates[0]
			if u.Rollback != tc.rollback || u.Version.Index != 5 || u.QueryRegistry || u.EncodedRegistryAuth != "" {
				t.Fatalf("update %+v", u)
			}
			if u.Spec.Labels[agents.LabelSpecHash] != hash {
				t.Fatalf("spec changed: %+v", u.Spec.Labels)
			}
		})
	}
}

// Right after an adopt the previous spec is the CLI's, without the hash.
func adoptedWith() swarm.Service {
	s := ownedWith(swarm.UpdateStateCompleted, false)
	prev := cliService("1", "shop_web", "shop", 4, nil).Spec
	s.PreviousSpec = &prev
	return s
}
