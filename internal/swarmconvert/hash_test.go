package swarmconvert

import (
	"testing"

	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/agents"
)

func TestSpecHash(t *testing.T) {
	three, five := uint64(3), uint64(5)
	base := func() swarm.ServiceSpec {
		return swarm.ServiceSpec{
			Annotations:  swarm.Annotations{Name: "web_app", Labels: map[string]string{LabelNamespace: "web"}},
			TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: "nginx:1"}},
			Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &three}},
		}
	}
	h := SpecHash(base())
	tests := []struct {
		name   string
		mutate func(*swarm.ServiceSpec)
		same   bool
	}{
		{"replicas don't count", func(s *swarm.ServiceSpec) { s.Mode.Replicated.Replicas = &five }, true},
		{"the label itself doesn't count", func(s *swarm.ServiceSpec) { s.Labels[agents.LabelSpecHash] = "x" }, true},
		{"image counts", func(s *swarm.ServiceSpec) { s.TaskTemplate.ContainerSpec.Image = "nginx:2" }, false},
		{"mode counts", func(s *swarm.ServiceSpec) { s.Mode = swarm.ServiceMode{Global: &swarm.GlobalService{}} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base()
			tt.mutate(&s)
			if got := SpecHash(s); (got == h) != tt.same {
				t.Fatalf("hash %s vs %s, same = %t", got, h, tt.same)
			}
		})
	}
	orig := base()
	labelled := WithSpecHash(orig)
	if labelled.Labels[agents.LabelSpecHash] != h || orig.Labels[agents.LabelSpecHash] != "" || *orig.Mode.Replicated.Replicas != 3 {
		t.Fatalf("labelled %v, original %v", labelled.Labels, orig.Labels)
	}
}
