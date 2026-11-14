package agents

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
)

func TestNormalizeArch(t *testing.T) {
	cases := []struct{ in, want string }{
		{"x86_64", "amd64"},
		{"amd64", "amd64"},
		{"aarch64", "arm64"},
		{"ARM64", "arm64"},
		{"armv7l", "arm/v7"},
		{"armv6l", "arm/v6"},
		{"i686", "386"},
		{"ppc64le", "ppc64le"},
		{"s390x", "s390x"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeArch(c.in); got != c.want {
			t.Errorf("NormalizeArch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDaemonInfoFrom(t *testing.T) {
	cases := []struct {
		name string
		info system.Info
		want *DaemonInfo
	}{
		{
			name: "no swarm",
			info: system.Info{OSType: "linux", Architecture: "x86_64", Swarm: swarm.Info{LocalNodeState: "inactive"}},
			want: &DaemonInfo{OS: "linux", Arch: "amd64", Swarm: &SwarmState{NodeState: "inactive"}},
		},
		{
			name: "manager",
			info: system.Info{OSType: "linux", Architecture: "aarch64", Swarm: swarm.Info{
				NodeID: "n1abc", LocalNodeState: "active", ControlAvailable: true,
				Cluster: &swarm.ClusterInfo{ID: "c1xyz"},
			}},
			want: &DaemonInfo{OS: "linux", Arch: "arm64", Swarm: &SwarmState{NodeID: "n1abc", NodeState: "active", Role: "manager", ClusterID: "c1xyz"}},
		},
		{
			name: "worker has no cluster info",
			info: system.Info{OSType: "linux", Architecture: "x86_64", Swarm: swarm.Info{NodeID: "n2", LocalNodeState: "active"}},
			want: &DaemonInfo{OS: "linux", Arch: "amd64", Swarm: &SwarmState{NodeID: "n2", NodeState: "active", Role: "worker"}},
		},
		{
			name: "pending join has no role",
			info: system.Info{OSType: "linux", Architecture: "x86_64", Swarm: swarm.Info{LocalNodeState: "pending", ControlAvailable: true}},
			want: &DaemonInfo{OS: "linux", Arch: "amd64", Swarm: &SwarmState{NodeState: "pending"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DaemonInfoFrom(c.info); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v / %+v, want %+v / %+v", got, got.Swarm, c.want, c.want.Swarm)
			}
		})
	}
}

func TestDaemonInfoSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   *DaemonInfo
		want *DaemonInfo
	}{
		{"nil stays nil", nil, nil},
		{
			name: "valid values kept",
			in:   &DaemonInfo{OS: "linux", Arch: "arm/v7", Swarm: &SwarmState{NodeID: "abc123", NodeState: "active", Role: "manager", ClusterID: "def456"}},
			want: &DaemonInfo{OS: "linux", Arch: "arm/v7", Swarm: &SwarmState{NodeID: "abc123", NodeState: "active", Role: "manager", ClusterID: "def456"}},
		},
		{
			name: "markup, unknown enums and long values dropped",
			in: &DaemonInfo{OS: "<script>", Arch: strings.Repeat("a", 33), Swarm: &SwarmState{
				NodeID: "abc'; DROP", NodeState: "leader", Role: "admin", ClusterID: strings.Repeat("c", 65),
			}},
			want: &DaemonInfo{Swarm: &SwarmState{}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.Sanitize(); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestDaemonInfoStoredPerAgent(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	created, err := svc.Create(ctx, "edge-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Enroll(ctx, enrollReq(created.Token)); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Get(ctx, created.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	reported := &DaemonInfo{OS: "linux", Arch: "arm64", Swarm: &SwarmState{NodeID: "n1", NodeState: "active", Role: "worker"}}

	cases := []struct {
		name  string
		hello HelloPayload
		want  *DaemonInfo
	}{
		{"new agent", HelloPayload{Daemon: reported}, reported},
		{"older agent clears the report", HelloPayload{}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ag := newConnectedAgent(a.ID, a.Name, func() {})
			ag.certFP = a.CertFingerprint
			ag.Hello = c.hello
			if err := svc.markOnline(ctx, ag, c.hello); err != nil {
				t.Fatal(err)
			}
			live, err := svc.DaemonInfo(ctx, a.ID)
			if err != nil || !reflect.DeepEqual(live, c.want) {
				t.Fatalf("online: %+v, %v; want %+v", live, err, c.want)
			}
			svc.markOffline(ag) //nolint:contextcheck // runs when the connection is gone, without a request context
			stored, err := svc.DaemonInfo(ctx, a.ID)
			if err != nil || !reflect.DeepEqual(stored, c.want) {
				t.Fatalf("offline: %+v, %v; want %+v", stored, err, c.want)
			}
		})
	}
	if _, err := svc.DaemonInfo(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v, want ErrNotFound", err)
	}
}
