package agents

import (
	"testing"

	"github.com/moby/moby/api/types/swarm"
)

func TestServiceListCarriesSpecHash(t *testing.T) {
	const h = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	svc := func(id, hash string) swarm.Service {
		return swarm.Service{ID: id, Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{
			Name: "web_" + id, Labels: map[string]string{LabelSpecHash: hash},
		}}}
	}
	got := SwarmServicesFrom([]swarm.Service{svc("a", h), svc("b", "not-a-hash")}).Services
	if len(got) != 2 || got[0].SpecHash != h || got[1].SpecHash != "" {
		t.Fatalf("services %+v", got)
	}
}
