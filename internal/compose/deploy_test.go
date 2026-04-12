package compose

import (
	"testing"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/mount"
)

func TestParseEnvContent(t *testing.T) {
	in := "# comment\n\nFOO=bar\nBAZ=qux\nNOEQ\n"
	got := parseEnvContent(in)
	want := []string{"FOO=bar", "BAZ=qux"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func strp(s string) *string { return &s }

func TestServiceToContainerConfig(t *testing.T) {
	proj := &composetypes.Project{
		Name: "demo",
		Volumes: composetypes.Volumes{
			"data": composetypes.VolumeConfig{Name: ""}, // → demo_data
		},
	}
	svc := composetypes.ServiceConfig{
		Name:  "web",
		Image: "nginx:alpine",
		Environment: composetypes.MappingWithEquals{
			"FOO": strp("bar"),
			"BAZ": strp("qux"),
			"NIL": nil, // must be skipped
		},
		Command:    composetypes.ShellCommand{"nginx", "-g", "daemon off;"},
		Entrypoint: composetypes.ShellCommand{"/docker-entrypoint.sh"},
		Ports: []composetypes.ServicePortConfig{
			{Target: 80, Published: "8080", Protocol: "tcp"},
		},
		Volumes: []composetypes.ServiceVolumeConfig{
			{Type: composetypes.VolumeTypeVolume, Source: "data", Target: "/var/lib/data"},
			{Type: composetypes.VolumeTypeBind, Source: "/host/path", Target: "/etc/nginx", ReadOnly: true},
		},
		Labels: composetypes.Labels{
			"app": "web",
		},
		Restart:  "unless-stopped",
		Hostname: "web",
		User:     "1000:1000",
		Networks: map[string]*composetypes.ServiceNetworkConfig{
			"default": {Aliases: []string{"api"}},
		},
	}

	netNames := map[string]string{"default": "demo_default"}
	cfg, hostCfg, netCfg, err := serviceToContainerConfig(proj, svc, netNames)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Image != "nginx:alpine" {
		t.Errorf("image = %q", cfg.Image)
	}
	if len(cfg.Env) != 2 {
		t.Errorf("env len = %d, want 2 (nil must be skipped)", len(cfg.Env))
	}
	// Sorted → BAZ first, FOO second
	if cfg.Env[0] != "BAZ=qux" || cfg.Env[1] != "FOO=bar" {
		t.Errorf("env = %v", cfg.Env)
	}
	if cfg.Labels[LabelProject] != "demo" || cfg.Labels[LabelService] != "web" || cfg.Labels["app"] != "web" {
		t.Errorf("labels = %v", cfg.Labels)
	}
	if _, ok := cfg.ExposedPorts["80/tcp"]; !ok {
		t.Errorf("80/tcp not exposed: %v", cfg.ExposedPorts)
	}
	if len(hostCfg.PortBindings["80/tcp"]) != 1 || hostCfg.PortBindings["80/tcp"][0].HostPort != "8080" {
		t.Errorf("port binding = %v", hostCfg.PortBindings)
	}
	if len(hostCfg.Binds) != 1 || hostCfg.Binds[0] != "/host/path:/etc/nginx:ro" {
		t.Errorf("binds = %v", hostCfg.Binds)
	}
	if len(hostCfg.Mounts) != 1 {
		t.Fatalf("mounts len = %d", len(hostCfg.Mounts))
	}
	m := hostCfg.Mounts[0]
	if m.Type != mount.TypeVolume || m.Source != "demo_data" || m.Target != "/var/lib/data" {
		t.Errorf("mount = %+v", m)
	}
	if string(hostCfg.RestartPolicy.Name) != "unless-stopped" {
		t.Errorf("restart = %v", hostCfg.RestartPolicy.Name)
	}
	ep, ok := netCfg.EndpointsConfig["demo_default"]
	if !ok {
		t.Fatalf("network endpoint missing: %v", netCfg.EndpointsConfig)
	}
	if len(ep.Aliases) != 1 || ep.Aliases[0] != "api" {
		t.Errorf("aliases = %v", ep.Aliases)
	}
}

func TestServiceToContainerConfig_NoImage(t *testing.T) {
	// Guard: serviceToContainerConfig itself doesn't require image, but
	// deployService does. This test documents that the translator still works
	// without an image so the caller catches it earlier.
	proj := &composetypes.Project{Name: "p"}
	svc := composetypes.ServiceConfig{Name: "s", Image: ""}
	cfg, _, _, err := serviceToContainerConfig(proj, svc, nil)
	if err != nil {
		t.Fatalf("translator should not fail on empty image: %v", err)
	}
	if cfg.Image != "" {
		t.Errorf("image = %q", cfg.Image)
	}
}
