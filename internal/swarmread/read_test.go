package swarmread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

type fakeDaemon struct {
	state    swarm.LocalNodeState
	control  bool
	service  swarm.Service
	status   *swarm.ServiceStatus
	calls    int
	block    bool
	deadline time.Duration
	filters  client.Filters
}

func (f *fakeDaemon) Info(ctx context.Context, _ client.InfoOptions) (client.SystemInfoResult, error) {
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	return client.SystemInfoResult{Info: system.Info{Swarm: swarm.Info{LocalNodeState: f.state, ControlAvailable: f.control}}}, nil
}

func (f *fakeDaemon) wait(ctx context.Context) error {
	f.calls++
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *fakeDaemon) NodeList(ctx context.Context, _ client.NodeListOptions) (client.NodeListResult, error) {
	if err := f.wait(ctx); err != nil {
		return client.NodeListResult{}, err
	}
	return client.NodeListResult{Items: []swarm.Node{{ID: "n1", Spec: swarm.NodeSpec{Role: swarm.NodeRoleManager}}}}, nil
}

// ServiceList fills task counts only with Status set, and only the list
// does, as dockerd does.
func (f *fakeDaemon) ServiceList(ctx context.Context, o client.ServiceListOptions) (client.ServiceListResult, error) {
	if err := f.wait(ctx); err != nil {
		return client.ServiceListResult{}, err
	}
	if ids := o.Filters.Clone()["id"]; len(ids) > 0 && !ids[f.service.ID] {
		return client.ServiceListResult{}, nil
	}
	s := f.service
	if o.Status {
		s.ServiceStatus = f.status
	}
	return client.ServiceListResult{Items: []swarm.Service{s}}, nil
}

func (f *fakeDaemon) ServiceInspect(ctx context.Context, id string, _ client.ServiceInspectOptions) (client.ServiceInspectResult, error) {
	if err := f.wait(ctx); err != nil {
		return client.ServiceInspectResult{}, err
	}
	if id != f.service.ID && id != f.service.Spec.Name {
		return client.ServiceInspectResult{}, cerrdefs.ErrNotFound.WithMessage("service " + id + " not found")
	}
	return client.ServiceInspectResult{Service: f.service}, nil
}

func (f *fakeDaemon) TaskList(ctx context.Context, o client.TaskListOptions) (client.TaskListResult, error) {
	if err := f.wait(ctx); err != nil {
		return client.TaskListResult{}, err
	}
	f.filters = o.Filters
	return client.TaskListResult{Items: []swarm.Task{{ID: "t1", ServiceID: "s1", NodeID: "n1", Status: swarm.TaskStatus{State: swarm.TaskStateRunning}}}}, nil
}

func (f *fakeDaemon) NetworkList(ctx context.Context, _ client.NetworkListOptions) (client.NetworkListResult, error) {
	if err := f.wait(ctx); err != nil {
		return client.NetworkListResult{}, err
	}
	return client.NetworkListResult{Items: []network.Summary{
		{Network: network.Network{ID: "o1", Name: "web_net", Scope: "swarm", Driver: "overlay", Options: map[string]string{"encrypted": ""}}},
		{Network: network.Network{ID: "b1", Name: "bridge", Scope: "local", Driver: "bridge"}},
	}}, nil
}

// secretService carries values in every place a spec can hold them.
func secretService() swarm.Service {
	return swarm.Service{
		ID: "s1",
		Spec: swarm.ServiceSpec{
			Annotations: swarm.Annotations{Name: "web_app", Labels: map[string]string{"com.docker.stack.namespace": "web"}},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
				Image:   "nginx:1",
				Env:     []string{"DB_PASSWORD=hunter2-env", "PLAIN"},
				Command: []string{"run", "--token=hunter2-cmd"},
				Args:    []string{"--password", "hunter2-arg"},
				Secrets: []*swarm.SecretReference{{SecretID: "sec1", SecretName: "db_pw", File: &swarm.SecretReferenceFileTarget{Name: "db_pw"}}},
				Configs: []*swarm.ConfigReference{{ConfigID: "cfg1", ConfigName: "nginx_conf", File: &swarm.ConfigReferenceFileTarget{Name: "/etc/nginx.conf"}}},
			}, LogDriver: &swarm.Driver{Name: "x", Options: map[string]string{"key": "hunter2-log"}}},
			Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}},
		},
	}
}

// Every Swarm frame is refused unless the daemon is a manager of an
// active Swarm, and the daemon is not asked for anything then.
func TestReadRefusedOffManagers(t *testing.T) {
	frames := []string{agents.FrameReqSwarmNodes, agents.FrameReqSwarmServices, agents.FrameReqSwarmServiceInspect,
		agents.FrameReqSwarmTasks, agents.FrameReqSwarmNetworks}
	states := []struct {
		name    string
		state   swarm.LocalNodeState
		control bool
	}{
		{"worker", swarm.LocalNodeStateActive, false},
		{"not in a swarm", swarm.LocalNodeStateInactive, false},
		{"locked manager", swarm.LocalNodeStateLocked, true},
		{"pending", swarm.LocalNodeStatePending, true},
	}
	for _, st := range states {
		for _, fr := range frames {
			t.Run(st.name+"/"+fr, func(t *testing.T) {
				d := &fakeDaemon{state: st.state, control: st.control, service: secretService()}
				_, err := Read(context.Background(), d, fr, json.RawMessage(`{"id":"s1"}`))
				if !errors.Is(err, agents.ErrNotSwarmManager) {
					t.Fatalf("err %v, want not a manager", err)
				}
				if d.calls != 0 {
					t.Fatalf("daemon asked %d times", d.calls)
				}
			})
		}
	}
}

func TestReadOnManager(t *testing.T) {
	d := &fakeDaemon{state: swarm.LocalNodeStateActive, control: true, service: secretService(),
		status: &swarm.ServiceStatus{RunningTasks: 3, DesiredTasks: 4}}
	ctx := context.Background()

	tests := []struct {
		name    string
		frame   string
		payload string
		want    string
		wantErr error
	}{
		{"nodes", agents.FrameReqSwarmNodes, "", `"id":"n1"`, nil},
		{"services", agents.FrameReqSwarmServices, "", `"name":"web_app"`, nil},
		{"inspect by name", agents.FrameReqSwarmServiceInspect, `{"id":"web_app"}`, `"env_keys":["DB_PASSWORD","PLAIN"]`, nil},
		{"inspect counts tasks", agents.FrameReqSwarmServiceInspect, `{"id":"web_app"}`, `"running_tasks":3,"desired_tasks":4`, nil},
		{"services count tasks", agents.FrameReqSwarmServices, "", `"running_tasks":3,"desired_tasks":4`, nil},
		{"inspect unknown", agents.FrameReqSwarmServiceInspect, `{"id":"nope"}`, "", agents.ErrSwarmServiceNotFound},
		{"inspect bad id", agents.FrameReqSwarmServiceInspect, `{"id":"../x"}`, "", errInvalidID},
		{"inspect no payload", agents.FrameReqSwarmServiceInspect, ``, "", errInvalidID},
		{"tasks", agents.FrameReqSwarmTasks, "", `"id":"t1"`, nil},
		{"tasks of service", agents.FrameReqSwarmTasks, `{"service_id":"web_app"}`, `"id":"t1"`, nil},
		{"tasks bad filter", agents.FrameReqSwarmTasks, `{"service_id":"a b"}`, "", errInvalidID},
		{"networks", agents.FrameReqSwarmNetworks, "", `"encrypted":true`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Read(ctx, d, tt.frame, json.RawMessage(tt.payload))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			b, _ := json.Marshal(out)
			if !strings.Contains(string(b), tt.want) {
				t.Fatalf("%s lacks %s", b, tt.want)
			}
			if strings.Contains(string(b), `"bridge"`) {
				t.Fatalf("local network in %s", b)
			}
		})
	}
	if _, err := Read(ctx, d, agents.FrameReqSwarmTasks, json.RawMessage(`{"service_id":"web_app"}`)); err != nil {
		t.Fatal(err)
	}
	if got := d.filters.Clone(); len(got["service"]) != 1 || !got["service"]["web_app"] {
		t.Fatalf("task filter %v", got)
	}
}

// No value of an env variable, command, argument or driver option, and
// no secret or config data leave the manager; their names do.
func TestServiceInspectHasNoValues(t *testing.T) {
	d := &fakeDaemon{state: swarm.LocalNodeStateActive, control: true, service: secretService()}
	for _, fr := range []string{agents.FrameReqSwarmServiceInspect, agents.FrameReqSwarmServices} {
		out, err := Read(context.Background(), d, fr, json.RawMessage(`{"id":"s1"}`))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		if strings.Contains(string(b), "hunter2") {
			t.Fatalf("%s: a value leaked: %s", fr, b)
		}
		if fr == agents.FrameReqSwarmServiceInspect {
			for _, want := range []string{`"name":"db_pw"`, `"name":"nginx_conf"`, `"target":"/etc/nginx.conf"`} {
				if !strings.Contains(string(b), want) {
					t.Fatalf("%s lacks %s", b, want)
				}
			}
		}
	}
}

// A daemon that never answers is given up within Timeout.
func TestReadTimesOut(t *testing.T) {
	d := &fakeDaemon{state: swarm.LocalNodeStateActive, control: true, block: true}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Read(ctx, d, agents.FrameReqSwarmServices, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	d.block = false
	if _, err := Read(context.Background(), d, agents.FrameReqSwarmServices, nil); err != nil {
		t.Fatal(err)
	}
	if d.deadline <= 0 || d.deadline > Timeout {
		t.Fatalf("deadline %v, want at most %v", d.deadline, Timeout)
	}
}
