package swarmconvert

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/compose"
)

func convertYAML(t *testing.T, src string) (*Result, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := loadProject(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	return Convert(project, "unit")
}

func loadProject(dir, stack string) (*types.Project, error) {
	return compose.LoadProject(context.Background(), dir, stack, "")
}

func TestConvertRefuses(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"port range to one port", "services:\n  a:\n    image: alpine\n    ports:\n      - \"8080-8081:80\"\n", "not a single port"},
		{"published port out of range", "services:\n  a:\n    image: alpine\n    ports:\n      - target: 80\n        published: \"70000\"\n", "not a single port"},
		{"unknown restart condition", "services:\n  a:\n    image: alpine\n    deploy:\n      restart_policy:\n        condition: bogus\n", "restart_policy.condition"},
		{"replicas on a global service", "services:\n  a:\n    image: alpine\n    deploy:\n      mode: global\n      replicas: 2\n", "replicas can only be used"},
		{"unknown endpoint mode", "services:\n  a:\n    image: alpine\n    deploy:\n      endpoint_mode: rr\n", "endpoint_mode"},
		{"unknown failure action", "services:\n  a:\n    image: alpine\n    deploy:\n      update_config:\n        failure_action: retry\n", "update_config.failure_action"},
		{"rollback that rolls back", "services:\n  a:\n    image: alpine\n    deploy:\n      rollback_config:\n        failure_action: rollback\n", "rollback_config.failure_action"},
		{"too precise cpus", "services:\n  a:\n    image: alpine\n    deploy:\n      resources:\n        limits:\n          cpus: \"0.0000000001\"\n", "too precise"},
		{"two keys for one network", "services:\n  a:\n    image: alpine\n    networks: [n, m]\nnetworks:\n  n:\n    name: same\n  m:\n    name: same\n    internal: true\n", "both name the network same"},
		{"generic resource without a spec", "services:\n  a:\n    image: alpine\n    deploy:\n      resources:\n        reservations:\n          generic_resources:\n            - x-note: 1\n", "discrete_resource_spec"},
		{"bad subnet", "services:\n  a:\n    image: alpine\n    networks: [n]\nnetworks:\n  n:\n    ipam:\n      config:\n        - subnet: nonsense\n", "subnet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := convertYAML(t, tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestConvertEnvironmentWithoutValue(t *testing.T) {
	res, err := convertYAML(t, "services:\n  a:\n    image: alpine\n    environment:\n      - KEY_ONLY\n      - SET=1\n")
	if err != nil {
		t.Fatal(err)
	}
	got := res.Services[0].TaskTemplate.ContainerSpec.Env
	want := []string{"KEY_ONLY", "SET=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env %v, want %v", got, want)
	}
}

func TestConvertNamespaceLabelWins(t *testing.T) {
	res, err := convertYAML(t, "services:\n  a:\n    image: alpine\n    labels:\n      com.docker.stack.namespace: other\n    deploy:\n      labels:\n        com.docker.stack.namespace: other\n        com.docker.stack.image: other\n")
	if err != nil {
		t.Fatal(err)
	}
	s := res.Services[0]
	if s.Labels[LabelNamespace] != "unit" || s.Labels[LabelImage] != "alpine" || s.TaskTemplate.ContainerSpec.Labels[LabelNamespace] != "unit" {
		t.Fatalf("labels %v / %v", s.Labels, s.TaskTemplate.ContainerSpec.Labels)
	}
}

func TestConvertNeedsStackAndImage(t *testing.T) {
	for _, name := range []string{"", "a_b", "A", "-a"} {
		if _, err := Convert(nil, name); err == nil {
			t.Fatalf("stack name %q accepted", name)
		}
	}
	if _, err := convertYAML(t, "services:\n  a:\n    build: .\n"); err == nil || !strings.Contains(err.Error(), "no image") {
		t.Fatalf("err %v, want no image", err)
	}
}

func TestNanoCPUs(t *testing.T) {
	cases := []struct {
		in   types.NanoCPUs
		want int64
	}{
		{0, 0},
		{0.1, 100_000_000},
		{0.25, 250_000_000},
		{0.3, 300_000_000},
		{1.5, 1_500_000_000},
		{2, 2_000_000_000},
		{0.001, 1_000_000},
	}
	for _, tc := range cases {
		got, err := nanoCPUs(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("nanoCPUs(%v) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
}

func TestServiceNetworksAndAliases(t *testing.T) {
	res, err := convertYAML(t, "services:\n  a:\n    image: alpine\n    networks:\n      n:\n        aliases: [x]\n      default: {}\nnetworks:\n  n:\n    name: shared-name\n")
	if err != nil {
		t.Fatal(err)
	}
	got := res.Services[0].TaskTemplate.Networks
	want := []swarm.NetworkAttachmentConfig{
		{Target: "shared-name", Aliases: []string{"x", "a"}},
		{Target: "unit_default", Aliases: []string{"a"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("networks %+v, want %+v", got, want)
	}
	if len(res.Networks) != 2 || res.Networks[0].Driver != "overlay" {
		t.Fatalf("network creates %+v", res.Networks)
	}
}

func TestMountsRefused(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"tmpfs with bind options", "services:\n  a:\n    image: alpine\n    volumes:\n      - type: tmpfs\n        target: /t\n        bind:\n          propagation: shared\n", "incompatible"},
		{"npipe", "services:\n  a:\n    image: alpine\n    volumes:\n      - type: npipe\n        source: x\n        target: /t\n", "volume type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := convertYAML(t, tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A placement constraint on one node keeps node-local data in place, so
// it needs no warning.
func TestPinnedServiceHasNoNodeWarnings(t *testing.T) {
	res, err := convertYAML(t, "services:\n  a:\n    image: alpine\n    volumes:\n      - data:/d\n      - /srv/x:/x\n    deploy:\n      placement:\n        constraints: [\"node.hostname == n1\"]\nvolumes:\n  data: {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings %v", res.Warnings)
	}
}
