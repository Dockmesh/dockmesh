package swarmwrite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/agents"
)

const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeDaemon struct {
	worker     bool
	services   []swarm.Service
	networks   []network.Summary
	failCreate string
	netBusy    int
	writes     []string
	updates    []client.ServiceUpdateOptions
	creates    []client.ServiceCreateOptions
}

func (f *fakeDaemon) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	sw := swarm.Info{LocalNodeState: swarm.LocalNodeStateActive, ControlAvailable: !f.worker}
	return client.SystemInfoResult{Info: system.Info{Swarm: sw}}, nil
}

func prefixed(f client.Filters, name string) bool {
	for n := range f.Clone()["name"] {
		if strings.HasPrefix(name, n) {
			return true
		}
	}
	return false
}

func (f *fakeDaemon) ServiceList(_ context.Context, o client.ServiceListOptions) (client.ServiceListResult, error) {
	var out []swarm.Service
	for _, s := range f.services {
		if prefixed(o.Filters, s.Spec.Name) {
			out = append(out, s)
		}
	}
	return client.ServiceListResult{Items: out}, nil
}

func (f *fakeDaemon) ServiceCreate(_ context.Context, o client.ServiceCreateOptions) (client.ServiceCreateResult, error) {
	if o.Spec.Name == f.failCreate {
		return client.ServiceCreateResult{}, errors.New("no such image")
	}
	f.writes = append(f.writes, "create "+o.Spec.Name)
	f.creates = append(f.creates, o)
	return client.ServiceCreateResult{ID: "id-" + o.Spec.Name}, nil
}

func (f *fakeDaemon) ServiceUpdate(_ context.Context, id string, o client.ServiceUpdateOptions) (client.ServiceUpdateResult, error) {
	f.writes = append(f.writes, "update "+id)
	f.updates = append(f.updates, o)
	return client.ServiceUpdateResult{}, nil
}

func (f *fakeDaemon) ServiceRemove(_ context.Context, id string, _ client.ServiceRemoveOptions) (client.ServiceRemoveResult, error) {
	f.writes = append(f.writes, "rm "+id)
	return client.ServiceRemoveResult{}, nil
}

func (f *fakeDaemon) NetworkList(_ context.Context, o client.NetworkListOptions) (client.NetworkListResult, error) {
	var out []network.Summary
	for _, n := range f.networks {
		if prefixed(o.Filters, n.Name) {
			out = append(out, n)
		}
	}
	return client.NetworkListResult{Items: out}, nil
}

func (f *fakeDaemon) NetworkCreate(_ context.Context, name string, o client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	if o.Scope != "swarm" || o.Labels[agents.LabelStackNamespace] == "" {
		return client.NetworkCreateResult{}, errors.New("bad create")
	}
	f.writes = append(f.writes, "netcreate "+name)
	return client.NetworkCreateResult{ID: "net-" + name}, nil
}

func (f *fakeDaemon) NetworkRemove(_ context.Context, id string, _ client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	if f.netBusy > 0 {
		f.netBusy--
		return client.NetworkRemoveResult{}, errors.New("network has active endpoints")
	}
	if id == "gone" {
		return client.NetworkRemoveResult{}, cerrdefs.ErrNotFound
	}
	f.writes = append(f.writes, "netrm "+id)
	return client.NetworkRemoveResult{}, nil
}

func svcSpec(stack, name string) swarm.ServiceSpec {
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name:   stack + "_" + name,
			Labels: map[string]string{agents.LabelStackNamespace: stack, agents.LabelSpecHash: hash},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:      "nginx:1",
				Labels:     map[string]string{agents.LabelStackNamespace: stack},
				Privileges: &swarm.Privileges{},
			},
			Networks: []swarm.NetworkAttachmentConfig{{Target: stack + "_default"}},
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}},
	}
}

func deployReq(services ...string) agents.SwarmDeployReq {
	const stack = "web"
	r := agents.SwarmDeployReq{
		Stack: stack,
		Networks: []network.CreateRequest{{
			Name: stack + "_default", Driver: "overlay",
			Labels: map[string]string{agents.LabelStackNamespace: stack},
		}},
	}
	for _, s := range services {
		r.Services = append(r.Services, agents.SwarmServiceWrite{Spec: svcSpec(stack, s), RegistryAuth: "auth"})
	}
	return r
}

func ptr[T any](v T) *T { return &v }

func TestDeploy(t *testing.T) {
	own := map[string]string{agents.LabelStackNamespace: "web"}
	other := map[string]string{agents.LabelStackNamespace: "other"}
	tests := []struct {
		name    string
		daemon  fakeDaemon
		req     func() agents.SwarmDeployReq
		wantErr string
		writes  []string
		check   func(t *testing.T, f *fakeDaemon, resp agents.SwarmDeployResp)
	}{
		{
			name:   "fresh stack creates network then services",
			req:    func() agents.SwarmDeployReq { return deployReq("app", "db") },
			writes: []string{"netcreate web_default", "create web_app", "create web_db"},
			check: func(t *testing.T, f *fakeDaemon, resp agents.SwarmDeployResp) {
				if !f.creates[0].QueryRegistry || f.creates[0].EncodedRegistryAuth != "auth" {
					t.Errorf("create options %+v: want digest pinning and registry auth", f.creates[0])
				}
				if resp.Error != "" || len(resp.Services) != 2 || resp.Services[0].Action != agents.SwarmActionCreated {
					t.Errorf("resp %+v", resp)
				}
			},
		},
		{
			name: "existing service is updated with its version, replicas kept",
			daemon: fakeDaemon{
				networks: []network.Summary{{Network: network.Network{Name: "web_default", ID: "n1", Driver: "overlay", Labels: own}}},
				services: []swarm.Service{{
					ID: "s1", Meta: swarm.Meta{Version: swarm.Version{Index: 7}},
					Spec: swarm.ServiceSpec{
						Annotations:  swarm.Annotations{Name: "web_app", Labels: own},
						Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: ptr(uint64(4))}},
						TaskTemplate: swarm.TaskSpec{ForceUpdate: 3},
					},
				}},
			},
			req:    func() agents.SwarmDeployReq { return deployReq("app") },
			writes: []string{"update s1"},
			check: func(t *testing.T, f *fakeDaemon, resp agents.SwarmDeployResp) {
				u := f.updates[0]
				if u.Version.Index != 7 || *u.Spec.Mode.Replicated.Replicas != 4 || u.Spec.TaskTemplate.ForceUpdate != 3 || !u.QueryRegistry {
					t.Errorf("update options %+v", u)
				}
				if resp.Networks[0].Action != agents.SwarmActionExists {
					t.Errorf("network %+v", resp.Networks[0])
				}
			},
		},
		{
			name: "existing network with drift is kept and warned about",
			daemon: fakeDaemon{networks: []network.Summary{{Network: network.Network{
				Name: "web_default", ID: "n1", Driver: "overlay", Internal: true, Labels: own,
			}}}},
			req:    func() agents.SwarmDeployReq { return deployReq("app") },
			writes: []string{"create web_app"},
			check: func(t *testing.T, _ *fakeDaemon, resp agents.SwarmDeployResp) {
				if len(resp.Networks[0].Warnings) != 1 {
					t.Errorf("warnings %v", resp.Networks[0].Warnings)
				}
			},
		},
		{
			name: "network of another stack refuses before any write",
			daemon: fakeDaemon{networks: []network.Summary{{Network: network.Network{
				Name: "web_default", Driver: "overlay", Labels: other,
			}}}},
			req:     func() agents.SwarmDeployReq { return deployReq("app") },
			wantErr: "network web_default exists and is not part of stack web",
		},
		{
			name: "service without the stack label refuses before any write",
			daemon: fakeDaemon{services: []swarm.Service{{
				ID: "s9", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web_db"}},
			}}},
			req:     func() agents.SwarmDeployReq { return deployReq("app", "db") },
			wantErr: "service web_db exists and is not part of stack web",
		},
		{
			name: "missing external network refuses",
			req: func() agents.SwarmDeployReq {
				r := deployReq("app")
				r.ExternalNetworks = []string{"shared"}
				return r
			},
			wantErr: "external network shared not found",
		},
		{
			name:   "local external network refuses",
			daemon: fakeDaemon{networks: []network.Summary{{Network: network.Network{Name: "shared", Scope: "local"}}}},
			req: func() agents.SwarmDeployReq {
				r := deployReq("app")
				r.ExternalNetworks = []string{"shared"}
				return r
			},
			wantErr: "not scoped to the Swarm",
		},
		{
			name:    "worker refuses",
			daemon:  fakeDaemon{worker: true},
			req:     func() agents.SwarmDeployReq { return deployReq("app") },
			wantErr: agents.ErrNotSwarmManager.Error(),
		},
		{
			name:   "failed write stops and reports what was done",
			daemon: fakeDaemon{failCreate: "web_b"},
			req:    func() agents.SwarmDeployReq { return deployReq("a", "b", "c") },
			writes: []string{"netcreate web_default", "create web_a"},
			check: func(t *testing.T, _ *fakeDaemon, resp agents.SwarmDeployResp) {
				if !strings.Contains(resp.Error, "web_b") || len(resp.Services) != 2 || resp.Services[1].Action != agents.SwarmActionFailed {
					t.Errorf("resp %+v", resp)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.daemon
			resp, err := Deploy(context.Background(), &f, tt.req())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if len(f.writes) != 0 {
					t.Fatalf("writes %v after a refusal", f.writes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(f.writes, ",") != strings.Join(tt.writes, ",") {
				t.Fatalf("writes = %v, want %v", f.writes, tt.writes)
			}
			if tt.check != nil {
				tt.check(t, &f, resp)
			}
		})
	}
}

func TestValidateDeploy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(r *agents.SwarmDeployReq)
		want   string
	}{
		{"ok", func(*agents.SwarmDeployReq) {}, ""},
		{"bad stack", func(r *agents.SwarmDeployReq) { r.Stack = "Web" }, "invalid stack name"},
		{"no services", func(r *agents.SwarmDeployReq) { r.Services = nil }, "1 to"},
		{"foreign name", func(r *agents.SwarmDeployReq) { r.Services[0].Spec.Name = "other_app" }, "must start with web_"},
		{"no stack label", func(r *agents.SwarmDeployReq) { delete(r.Services[0].Spec.Labels, agents.LabelStackNamespace) }, "lacks the stack label"},
		{"no hash", func(r *agents.SwarmDeployReq) { r.Services[0].Spec.Labels[agents.LabelSpecHash] = "x" }, "spec hash"},
		{"plugin", func(r *agents.SwarmDeployReq) { r.Services[0].Spec.TaskTemplate.PluginSpec = &swarm.RuntimeSpec{} }, "only container"},
		{"no new privileges", func(r *agents.SwarmDeployReq) {
			r.Services[0].Spec.TaskTemplate.ContainerSpec.Privileges.NoNewPrivileges = true
		}, "privileges"},
		{"cap add", func(r *agents.SwarmDeployReq) {
			r.Services[0].Spec.TaskTemplate.ContainerSpec.CapabilityAdd = []string{"SYS_ADMIN"}
		}, "container options"},
		{"relative bind", func(r *agents.SwarmDeployReq) {
			r.Services[0].Spec.TaskTemplate.ContainerSpec.Mounts = []mount.Mount{{Type: "bind", Source: "data"}}
		}, "absolute"},
		{"npipe", func(r *agents.SwarmDeployReq) {
			r.Services[0].Spec.TaskTemplate.ContainerSpec.Mounts = []mount.Mount{{Type: "npipe", Source: "x"}}
		}, "mount type"},
		{"unknown network", func(r *agents.SwarmDeployReq) {
			r.Services[0].Spec.TaskTemplate.Networks = []swarm.NetworkAttachmentConfig{{Target: "host"}}
		}, "doesn't name"},
		{"bridge network", func(r *agents.SwarmDeployReq) { r.Networks[0].Driver = "bridge" }, "only overlay"},
		{"ingress network", func(r *agents.SwarmDeployReq) { r.Networks[0].Ingress = true }, "ingress"},
		{"network without label", func(r *agents.SwarmDeployReq) { r.Networks[0].Labels = nil }, "lacks the stack label"},
		{"duplicate service", func(r *agents.SwarmDeployReq) { r.Services = append(r.Services, r.Services[0]) }, "listed twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := deployReq("app")
			tt.mutate(&r)
			err := r.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) || !errors.Is(err, agents.ErrSwarmWriteRefused) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRemove(t *testing.T) {
	own := map[string]string{agents.LabelStackNamespace: "web"}
	tests := []struct {
		name    string
		daemon  fakeDaemon
		req     agents.SwarmRemoveReq
		wantErr string
		writes  []string
		actions []string
	}{
		{
			name: "services before networks, gone ones missing",
			daemon: fakeDaemon{
				services: []swarm.Service{{ID: "s1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web_app", Labels: own}}}},
				networks: []network.Summary{{Network: network.Network{Name: "web_default", ID: "n1", Labels: own}}},
				netBusy:  2,
			},
			req:     agents.SwarmRemoveReq{Stack: "web", Services: []string{"web_app", "web_old"}, Networks: []string{"web_default"}},
			writes:  []string{"rm s1", "netrm n1"},
			actions: []string{"removed", "missing", "removed"},
		},
		{
			name:    "network gone meanwhile counts as removed",
			daemon:  fakeDaemon{networks: []network.Summary{{Network: network.Network{Name: "web_x", ID: "gone", Labels: own}}}},
			req:     agents.SwarmRemoveReq{Stack: "web", Networks: []string{"web_x"}},
			actions: []string{"removed"},
		},
		{
			name: "service of another stack refuses before any removal",
			daemon: fakeDaemon{services: []swarm.Service{
				{ID: "s1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web_app", Labels: own}}},
				{ID: "s2", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web_x"}}},
			}},
			req:     agents.SwarmRemoveReq{Stack: "web", Services: []string{"web_app", "web_x"}},
			wantErr: "service web_x is not part of stack web",
		},
		{
			name:    "network of another stack refuses",
			daemon:  fakeDaemon{networks: []network.Summary{{Network: network.Network{Name: "shared", ID: "n2"}}}},
			req:     agents.SwarmRemoveReq{Stack: "web", Networks: []string{"shared"}},
			wantErr: "network shared is not part of stack web",
		},
		{
			name:    "name outside the stack refuses",
			req:     agents.SwarmRemoveReq{Stack: "web", Services: []string{"api_app"}},
			wantErr: "must start with web_",
		},
		{
			name:    "worker refuses",
			daemon:  fakeDaemon{worker: true},
			req:     agents.SwarmRemoveReq{Stack: "web", Services: []string{"web_app"}},
			wantErr: agents.ErrNotSwarmManager.Error(),
		},
	}
	old := networkRemoveWait
	networkRemoveWait = time.Millisecond
	t.Cleanup(func() { networkRemoveWait = old })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.daemon
			resp, err := Remove(context.Background(), &f, tt.req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if len(f.writes) != 0 {
					t.Fatalf("writes %v after a refusal", f.writes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(f.writes, ",") != strings.Join(tt.writes, ",") {
				t.Fatalf("writes = %v, want %v", f.writes, tt.writes)
			}
			var got []string
			for _, r := range append(resp.Services, resp.Networks...) {
				got = append(got, r.Action)
			}
			if strings.Join(got, ",") != strings.Join(tt.actions, ",") {
				t.Fatalf("actions = %v, want %v", got, tt.actions)
			}
		})
	}
}
