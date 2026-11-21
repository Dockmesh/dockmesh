package agents

import (
	"testing"

	"github.com/moby/moby/api/types/network"
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

func TestNetworkListMarksDockmeshNetworks(t *testing.T) {
	nw := func(name string, labels map[string]string) network.Summary {
		return network.Summary{Network: network.Network{Name: name, ID: "id-" + name, Scope: "swarm", Labels: labels}}
	}
	got := SwarmNetworksFrom([]network.Summary{
		nw("web_default", map[string]string{LabelStackNamespace: "web", LabelDockmeshStack: "web"}),
		nw("web_cli", map[string]string{LabelStackNamespace: "web"}),
		nw("web_odd", map[string]string{LabelStackNamespace: "web", LabelDockmeshStack: "api"}),
	}).Networks
	if len(got) != 3 || !got[0].Dockmesh || got[1].Dockmesh || got[2].Dockmesh {
		t.Fatalf("networks %+v", got)
	}
}
