package topology

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

func TestBuild(t *testing.T) {
	nets := []network.Inspect{
		{Network: network.Network{ID: "n-web", Name: "web_default", Driver: "bridge", Scope: "local", Labels: map[string]string{stackLabel: "web"}}},
		{Network: network.Network{ID: "n-bridge", Name: "bridge", Driver: "bridge", Scope: "local"}},
	}
	containers := []container.Summary{
		{
			ID: "c-app", Names: []string{"/web-app-1"}, State: container.StateRunning, Image: "nginx:1",
			Labels: map[string]string{stackLabel: "web"},
			Ports: []container.PortSummary{
				{PrivatePort: 80, PublicPort: 8080, Type: "tcp", IP: netip.MustParseAddr("0.0.0.0")},
				{PrivatePort: 80, PublicPort: 8080, Type: "tcp", IP: netip.MustParseAddr("::")},
				{PrivatePort: 443, Type: "tcp"},
			},
			NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{
				"web_default": {NetworkID: "n-web", IPAddress: netip.MustParseAddr("172.18.0.2")},
				"bridge":      {NetworkID: "n-bridge"},
				"gone":        {NetworkID: "n-removed"},
				"nil":         nil,
			}},
		},
		{ID: "c-solo", Names: []string{"/solo"}, State: container.StateExited, Image: "busybox"},
	}

	g := Build(nets, containers)

	wantNets := []Network{
		{ID: "n-web", Name: "web_default", Driver: "bridge", Scope: "local", Stack: "web"},
		{ID: "n-bridge", Name: "bridge", Driver: "bridge", Scope: "local", System: true},
	}
	if !reflect.DeepEqual(g.Networks, wantNets) {
		t.Errorf("networks = %+v", g.Networks)
	}
	wantCts := []Container{
		{ID: "c-app", Name: "web-app-1", State: "running", Image: "nginx:1", Stack: "web", Ports: []Port{{HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}},
		{ID: "c-solo", Name: "solo", State: "exited", Image: "busybox"},
	}
	if !reflect.DeepEqual(g.Containers, wantCts) {
		t.Errorf("containers = %+v", g.Containers)
	}
	wantLinks := []Link{
		{NetworkID: "n-bridge", ContainerID: "c-app"},
		{NetworkID: "n-web", ContainerID: "c-app", IPv4: "172.18.0.2"},
	}
	if !reflect.DeepEqual(g.Links, wantLinks) {
		t.Errorf("links = %+v", g.Links)
	}
}

func TestBuildEmptyListsAreNotNull(t *testing.T) {
	g := Build(nil, nil)
	if g.Networks == nil || g.Containers == nil || g.Links == nil {
		t.Fatalf("nil slice in %+v", g)
	}
}
