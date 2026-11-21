package swarmwrite

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func cliService(id, name, stack string, version uint64, labels map[string]string) swarm.Service {
	l := map[string]string{agents.LabelStackNamespace: stack}
	for k, v := range labels {
		l[k] = v
	}
	s := swarm.Service{ID: id, Spec: swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: name, Labels: l},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{Image: "nginx", Env: []string{"A=b"}},
			Networks:      []swarm.NetworkAttachmentConfig{{Target: "n-ext"}},
		},
	}}
	s.Version.Index = version
	return s
}

func TestExport(t *testing.T) {
	f := &fakeDaemon{
		services: []swarm.Service{
			cliService("2", "shop_web", "shop", 1, nil),
			cliService("1", "shop_db", "shop", 1, nil),
			cliService("3", "other_web", "other", 1, nil),
		},
		networks: []network.Summary{
			{Network: network.Network{ID: "n-own", Name: "shop_default", Labels: map[string]string{agents.LabelStackNamespace: "shop"}}},
			{Network: network.Network{ID: "n-ext", Name: "shared"}},
			{Network: network.Network{ID: "n-x", Name: "unrelated"}},
		},
	}
	out, err := Export(context.Background(), f, agents.SwarmExportReq{Stack: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Services) != 2 || out.Services[0].Spec.Name != "shop_db" || out.Services[0].Spec.TaskTemplate.ContainerSpec.Env[0] != "A=b" {
		t.Fatalf("services %+v", out.Services)
	}
	if len(out.Networks) != 2 {
		t.Fatalf("networks %+v", out.Networks)
	}
	if _, err := Export(context.Background(), f, agents.SwarmExportReq{Stack: "none"}); !errors.Is(err, agents.ErrSwarmWriteRefused) {
		t.Fatalf("no services: %v", err)
	}
	if _, err := Export(context.Background(), &fakeDaemon{worker: true}, agents.SwarmExportReq{Stack: "shop"}); !errors.Is(err, agents.ErrNotSwarmManager) {
		t.Fatalf("worker: %v", err)
	}
}

func TestAdopt(t *testing.T) {
	svcs := func() []swarm.Service {
		return []swarm.Service{
			cliService("1", "shop_web", "shop", 4, nil),
			cliService("2", "shop_db", "shop", 9, map[string]string{agents.LabelSpecHash: hash}),
			cliService("3", "shop_x", "other", 1, nil),
		}
	}
	req := func(svcs ...agents.SwarmAdoptService) agents.SwarmAdoptReq {
		return agents.SwarmAdoptReq{Stack: "shop", Services: svcs}
	}
	web := agents.SwarmAdoptService{ID: "1", Name: "shop_web", Version: 4, SpecHash: hash}
	db := agents.SwarmAdoptService{ID: "2", Name: "shop_db", Version: 3, SpecHash: hash}

	f := &fakeDaemon{services: svcs()}
	out, err := Adopt(context.Background(), f, req(web, db))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 1 || out.Services[0].Action != agents.SwarmActionUpdated || out.Services[1].Action != agents.SwarmActionExists {
		t.Fatalf("writes %v, out %+v", f.writes, out)
	}
	u := f.updates[0]
	if u.Version.Index != 4 || u.Spec.Labels[agents.LabelSpecHash] != hash || u.Spec.Labels[agents.LabelStackNamespace] != "shop" {
		t.Fatalf("update %+v", u)
	}
	if u.Spec.TaskTemplate.ContainerSpec.Env[0] != "A=b" || u.EncodedRegistryAuth != "" || u.QueryRegistry {
		t.Fatalf("the update changed more than the label: %+v", u)
	}

	for name, tc := range map[string]agents.SwarmAdoptReq{
		"changed since":  req(agents.SwarmAdoptService{ID: "1", Name: "shop_web", Version: 3, SpecHash: hash}),
		"replaced":       req(agents.SwarmAdoptService{ID: "9", Name: "shop_web", Version: 4, SpecHash: hash}),
		"other stack":    req(agents.SwarmAdoptService{ID: "3", Name: "shop_x", Version: 1, SpecHash: hash}),
		"gone":           req(agents.SwarmAdoptService{ID: "7", Name: "shop_gone", Version: 1, SpecHash: hash}),
		"bad hash":       req(agents.SwarmAdoptService{ID: "1", Name: "shop_web", Version: 4, SpecHash: "x"}),
		"outside prefix": req(agents.SwarmAdoptService{ID: "1", Name: "web", Version: 4, SpecHash: hash}),
	} {
		f := &fakeDaemon{services: svcs()}
		if _, err := Adopt(context.Background(), f, tc); !errors.Is(err, agents.ErrSwarmWriteRefused) {
			t.Errorf("%s: %v", name, err)
		}
		if len(f.writes) != 0 {
			t.Errorf("%s: wrote %v", name, f.writes)
		}
	}
}
