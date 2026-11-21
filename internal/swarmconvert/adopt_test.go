package swarmconvert

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/swarmgolden"
)

// runningFrom turns the CLI's recorded create requests into what a
// manager lists afterwards: networks by id, images pinned to a digest.
func runningFrom(c swarmgolden.Case) ([]swarm.Service, []network.Summary, map[string]string) {
	var nets []network.Summary
	ids := map[string]string{}
	names := map[string]string{}
	add := func(name string, labels map[string]string) {
		id := "id-" + name
		ids[name], names[id] = id, name
		nets = append(nets, network.Summary{Network: network.Network{ID: id, Name: name, Scope: "swarm", Driver: "overlay", Labels: labels}})
	}
	for _, n := range c.Networks {
		add(n.Name, n.Labels)
	}
	for _, n := range c.ExternalNetworks {
		add(n, nil)
	}
	var svcs []swarm.Service
	for i, spec := range c.Services {
		spec.TaskTemplate.Networks = append([]swarm.NetworkAttachmentConfig(nil), spec.TaskTemplate.Networks...)
		for j, a := range spec.TaskTemplate.Networks {
			if id, ok := ids[a.Target]; ok {
				spec.TaskTemplate.Networks[j].Target = id
			}
		}
		if cs := spec.TaskTemplate.ContainerSpec; cs != nil {
			cc := *cs
			cc.Image += "@sha256:" + strings.Repeat("a", 64)
			spec.TaskTemplate.ContainerSpec = &cc
		}
		svcs = append(svcs, swarm.Service{ID: "svc" + string(rune('a'+i)), Spec: spec})
	}
	return svcs, nets, names
}

func TestAdoptRoundTrip(t *testing.T) {
	cases, err := swarmgolden.LoadAll(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			svcs, nets, names := runningFrom(c)
			a, err := AdoptStack(c.Stack(), svcs, nets)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range a.Findings {
				t.Errorf("finding %s", f)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(a.Compose), 0o600); err != nil {
				t.Fatal(err)
			}
			project, err := compose.LoadProject(context.Background(), dir, c.Stack(), a.Env)
			if err != nil {
				t.Fatalf("load adopted file: %v\n%s", err, a.Compose)
			}
			res, err := Convert(project, c.Stack())
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Networks) != 0 {
				t.Errorf("an adopted stack creates networks: %v", res.Networks)
			}
			byName := map[string]swarm.ServiceSpec{}
			for _, s := range res.Services {
				byName[s.Name] = s
			}
			for _, s := range svcs {
				got, ok := byName[s.Spec.Name]
				if !ok {
					t.Errorf("service %s missing", s.Spec.Name)
					continue
				}
				if d := Diff(s.Spec, got, names); len(d) > 0 {
					t.Errorf("%s differs in %v\n%+v\n%+v", s.Spec.Name, d, s.Spec.EndpointSpec, got.EndpointSpec)
				}
			}
		})
	}
}

func TestAdoptEnvironmentStaysOutOfTheFile(t *testing.T) {
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: "s_app", Labels: map[string]string{LabelNamespace: "s"}},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
			Image: "alpine", Env: []string{"TOKEN=se$cret", "MULTI=a\nb", "BARE"}, Args: []string{"echo", "$HOME"},
		}},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}},
	}
	a, err := AdoptStack("s", []swarm.Service{{ID: "x", Spec: spec}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Compose, "se$cret") || strings.Contains(a.Compose, "se$$cret") {
		t.Fatalf("value in the compose file:\n%s", a.Compose)
	}
	if a.Env != "APP_TOKEN=se$cret\n" {
		t.Fatalf("env %q", a.Env)
	}
	if !strings.Contains(a.Compose, "$$HOME") {
		t.Fatalf("command not escaped:\n%s", a.Compose)
	}
	if len(a.Findings) != 1 || a.Findings[0].Key != "environment MULTI" {
		t.Fatalf("findings %v", a.Findings)
	}
}

func TestAdoptDriverOptionsGoToTheEnvironmentFile(t *testing.T) {
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: "s_app", Labels: map[string]string{LabelNamespace: "s"}},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{
			Image: "alpine",
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "s_data", Target: "/data", VolumeOptions: &mount.VolumeOptions{
				Labels:       map[string]string{LabelNamespace: "s"},
				DriverConfig: &mount.Driver{Name: "local", Options: map[string]string{"o": "username=u,password=pw"}},
			}}},
		}},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}},
	}
	a, err := AdoptStack("s", []swarm.Service{{ID: "x", Spec: spec}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Compose, "password=pw") || !strings.Contains(a.Env, "VOL_DATA_O=username=u,password=pw") {
		t.Fatalf("compose:\n%s\nenv %q", a.Compose, a.Env)
	}
}
