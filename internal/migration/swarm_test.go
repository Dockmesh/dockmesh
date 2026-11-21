package migration

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestSwarmScanCheck(t *testing.T) {
	task := map[string]string{labelStackNamespace: "web", "com.docker.swarm.service.name": "web_app", "com.docker.swarm.service.id": "s1"}
	composeWeb := map[string]string{labelComposeProject: "web"}
	running := container.StateRunning
	tests := []struct {
		name      string
		labels    []map[string]string
		state     container.ContainerState
		swarmNode bool
		want      error
	}{
		{"compose container", []map[string]string{composeWeb}, running, false, nil},
		{"task of the stack", []map[string]string{task}, running, false, ErrSwarmStack},
		{"task of another stack", []map[string]string{{labelStackNamespace: "other", "com.docker.swarm.service.name": "other_app", "com.docker.swarm.service.id": "s2"}}, running, false, nil},
		{"compose service with forged task labels", []map[string]string{{labelComposeProject: "evil", labelStackNamespace: "web", "com.docker.swarm.service.name": "web_app", "com.docker.swarm.service.id": "s1"}}, running, false, nil},
		{"namespace label without a service", []map[string]string{{labelStackNamespace: "web"}}, running, false, nil},
		{"swarm node, compose container here", []map[string]string{composeWeb}, running, true, nil},
		{"swarm node, nothing here", nil, running, true, ErrSwarmUnclear},
		{"plain host, nothing here", nil, running, false, nil},
		{"swarm node, compose container stopped", []map[string]string{composeWeb}, container.StateExited, true, ErrSwarmUnclear},
		{"task with the stack's compose project", []map[string]string{{labelComposeProject: "web", "com.docker.swarm.service.id": "s1"}}, running, false, ErrSwarmStack},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var list []container.Summary
			for _, l := range tt.labels {
				list = append(list, container.Summary{Labels: l, State: tt.state})
			}
			if got := (swarmScan{containers: list, swarmNode: tt.swarmNode}).check("web"); !errors.Is(got, tt.want) || (tt.want == nil) != (got == nil) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckTargetKind(t *testing.T) {
	tests := []struct {
		kind    string
		wantErr error
		ok      bool
	}{
		{"", nil, true},
		{TargetKindHost, nil, true},
		{TargetKindCluster, ErrClusterTarget, false},
		{"node", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			err := CheckTargetKind(tt.kind)
			if (err == nil) != tt.ok {
				t.Fatalf("err %v, want ok=%v", err, tt.ok)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("err %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// A Swarm stack never gets a migration record, so nothing is stopped.
func TestInitiateRefusesSwarmStack(t *testing.T) {
	e := newMigrationEnv(t)
	e.source.containers = []container.Summary{{Labels: map[string]string{labelStackNamespace: "web", "com.docker.swarm.service.name": "web_app", "com.docker.swarm.service.id": "s1"}}}
	_, err := e.svc.Initiate(context.Background(), "web", "agent1", "u1", "", nil)
	if !errors.Is(err, ErrSwarmStack) {
		t.Fatalf("err %v, want ErrSwarmStack", err)
	}
	list, err := e.svc.Store().ListAll(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 || e.source.stops != 0 {
		t.Fatalf("%d records, %d stops; want none", len(list), e.source.stops)
	}
}

func TestPreflightFailsSwarmStack(t *testing.T) {
	e := newMigrationEnv(t)
	e.source.containers = []container.Summary{{Labels: map[string]string{labelStackNamespace: "web", "com.docker.swarm.service.name": "web_app", "com.docker.swarm.service.id": "s1"}}}
	res, err := e.svc.Preflight(context.Background(), "web", "agent1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("preflight passed")
	}
	for _, c := range res.Checks {
		if c.Name == "swarm" && c.Status == CheckFail && c.Detail == ErrSwarmStack.Error() {
			return
		}
	}
	t.Fatalf("no failed swarm check: %+v", res.Checks)
}

// A stack deployed to a Swarm cluster is refused from its deployment
// target alone, before any host is asked.
func TestMigrationRefusesClusterTargetStack(t *testing.T) {
	e := newMigrationEnv(t)
	if _, err := e.db.Exec(`DELETE FROM stack_deployments WHERE stack_name = 'web'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('web', 'cluster', 'c1', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Initiate(context.Background(), "web", "agent1", "u1", "", nil); !errors.Is(err, ErrSwarmStack) {
		t.Fatalf("Initiate: err %v, want ErrSwarmStack", err)
	}
	res, err := e.svc.Preflight(context.Background(), "web", "agent1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed || len(res.Checks) != 1 || res.Checks[0].Name != "swarm" || res.Checks[0].Status != CheckFail {
		t.Fatalf("preflight %+v, want one failed swarm check", res)
	}
	if e.source.stops != 0 {
		t.Fatalf("%d stops, want none", e.source.stops)
	}
}
