package swarmconvert

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/swarmgolden"
)

func planYAML(t *testing.T, src string) *Plan {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := loadProject(dir, "unit")
	if err != nil {
		t.Fatal(err)
	}
	unresolved, _, err := compose.MergeEnvironment(context.Background(), dir, "unit", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	return NewPlan(project, unresolved, "unit")
}

// ruleCases holds one compose file per entry of Rules, with the finding
// the entry must produce.
var ruleCases = map[string]struct {
	src, service, key string
}{
	"build without image":                   {"services:\n  a:\n    build: .\n", "a", "build"},
	"privileged":                            {"services:\n  a:\n    image: alpine\n    privileged: true\n", "a", "privileged"},
	"devices":                               {"services:\n  a:\n    image: alpine\n    devices: [/dev/fuse]\n", "a", "devices"},
	"gpus":                                  {"services:\n  a:\n    image: alpine\n    gpus: all\n", "a", "gpus"},
	"deploy.resources.reservations.devices": {"services:\n  a:\n    image: alpine\n    deploy:\n      resources:\n        reservations:\n          devices:\n            - capabilities: [gpu]\n", "a", "deploy.resources.reservations.devices"},
	"network_mode":                          {"services:\n  a:\n    image: alpine\n    network_mode: host\n", "a", "network_mode"},
	"pid":                                   {"services:\n  a:\n    image: alpine\n    pid: host\n", "a", "pid"},
	"ipc":                                   {"services:\n  a:\n    image: alpine\n    ipc: host\n", "a", "ipc"},
	"volumes_from":                          {"services:\n  a:\n    image: alpine\n    volumes_from: [b]\n  b:\n    image: alpine\n", "a", "volumes_from"},
	"links":                                 {"services:\n  a:\n    image: alpine\n    links: [b]\n  b:\n    image: alpine\n", "a", "links"},
	"external_links":                        {"services:\n  a:\n    image: alpine\n    external_links: [db]\n", "a", "external_links"},
	"depends_on.condition":                  {"services:\n  a:\n    image: alpine\n    depends_on:\n      b:\n        condition: service_healthy\n  b:\n    image: alpine\n", "a", "depends_on"},
	"ports.host_ip":                         {"services:\n  a:\n    image: alpine\n    ports: [\"127.0.0.1:8080:80\"]\n", "a", "ports.host_ip"},
	"tmpfs":                                 {"services:\n  a:\n    image: alpine\n    tmpfs: [/run]\n", "a", "tmpfs"},
	"volumes.tmpfs.mode":                    {"services:\n  a:\n    image: alpine\n    volumes:\n      - type: tmpfs\n        target: /t\n        tmpfs:\n          mode: 0o700\n", "a", "volumes.tmpfs.mode"},
	"volumes relative bind":                 {"services:\n  a:\n    image: alpine\n    volumes: [./data:/data]\n", "a", "volumes /data"},
	"volumes relative bind device":          {"services:\n  a:\n    image: alpine\n    volumes: [data:/data]\nvolumes:\n  data:\n    driver_opts:\n      type: none\n      o: bind\n      device: ./data\n", "", "volumes.data"},
	"networks.ipv4_address":                 {"services:\n  a:\n    image: alpine\n    networks:\n      n:\n        ipv4_address: 198.18.0.5\nnetworks:\n  n:\n    ipam:\n      config:\n        - subnet: 198.18.0.0/24\n", "a", "networks.ipv4_address"},
	"networks.ipv6_address":                 {"services:\n  a:\n    image: alpine\n    networks:\n      n:\n        ipv6_address: 2001:db8::5\nnetworks:\n  n: {}\n", "a", "networks.ipv6_address"},
	"networks.mac_address":                  {"services:\n  a:\n    image: alpine\n    networks:\n      n:\n        mac_address: 02:42:ac:11:00:02\nnetworks:\n  n: {}\n", "a", "networks.mac_address"},
	"networks host":                         {"services:\n  a:\n    image: alpine\n    networks: [h]\nnetworks:\n  h:\n    external: true\n    name: host\n", "", "networks.h"},
	"secrets.*":                             {"services:\n  a:\n    image: alpine\n    secrets: [s]\nsecrets:\n  s:\n    environment: TOKEN\n", "", "secrets.s"},
	"configs.*":                             {"services:\n  a:\n    image: alpine\n    configs: [c]\nconfigs:\n  c:\n    content: hello\n", "", "configs.c"},
	"jobs.*":                                {"services:\n  a:\n    image: alpine\njobs:\n  j:\n    image: alpine\n    triggers:\n      manual: true\n", "", "jobs.j"},
	"models.*":                              {"services:\n  a:\n    image: alpine\nmodels:\n  m:\n    model: ai/x\n", "", "models.m"},

	"restart":                 {"services:\n  a:\n    image: alpine\n    restart: always\n", "a", "restart"},
	"build":                   {"services:\n  a:\n    image: alpine\n    build: .\n", "a", "build"},
	"container_name":          {"services:\n  a:\n    image: alpine\n    container_name: app-1\n", "a", "container_name"},
	"depends_on":              {"services:\n  a:\n    image: alpine\n    depends_on: [b]\n  b:\n    image: alpine\n", "a", "depends_on"},
	"expose":                  {"services:\n  a:\n    image: alpine\n    expose: [\"80\"]\n", "a", "expose"},
	"scale":                   {"services:\n  a:\n    image: alpine\n    scale: 2\n", "a", "scale"},
	"pull_policy":             {"services:\n  a:\n    image: alpine\n    pull_policy: always\n", "a", "pull_policy"},
	"ports.name":              {"services:\n  a:\n    image: alpine\n    ports:\n      - target: 80\n        name: web\n", "a", "ports.name"},
	"ports.app_protocol":      {"services:\n  a:\n    image: alpine\n    ports:\n      - target: 80\n        app_protocol: http\n", "a", "ports.app_protocol"},
	"volumes.bind.selinux":    {"services:\n  a:\n    image: alpine\n    volumes: [\"/srv/x:/x:z\"]\n", "a", "volumes.bind.selinux"},
	"networks.priority":       {"services:\n  a:\n    image: alpine\n    networks:\n      n:\n        priority: 5\nnetworks:\n  n: {}\n", "a", "networks.priority"},
	"networks.gw_priority":    {"services:\n  a:\n    image: alpine\n    networks:\n      n:\n        gw_priority: 5\nnetworks:\n  n: {}\n", "a", "networks.gw_priority"},
	"networks.interface_name": {"services:\n  a:\n    image: alpine\n    networks:\n      n:\n        interface_name: eth5\nnetworks:\n  n: {}\n", "a", "networks.interface_name"},
	"networks.link_local_ips": {"services:\n  a:\n    image: alpine\n    networks:\n      n:\n        link_local_ips: [169.254.8.8]\nnetworks:\n  n: {}\n", "a", "networks.link_local_ips"},
	"networks.*":              {"services:\n  a:\n    image: alpine\nnetworks:\n  spare: {}\n", "", "networks.spare"},
	"networks other name":     {"services:\n  a:\n    image: alpine\n    networks: [n]\nnetworks:\n  n:\n    name: shared\n", "", "networks.n"},
	"volumes.*":               {"services:\n  a:\n    image: alpine\nvolumes:\n  spare: {}\n", "", "volumes.spare"},
	"volumes other name":      {"services:\n  a:\n    image: alpine\n    volumes: [vol:/v]\nvolumes:\n  vol:\n    name: other_data\n", "", "volumes.vol"},
}

// ruleOf maps a case name to its rule; extra cases for one rule add a
// suffix after the rule key.
func ruleOf(caseName string) string {
	if caseName == "volumes relative bind device" {
		return "volumes relative bind"
	}
	return caseName
}

func TestEveryRule(t *testing.T) {
	for name, tc := range ruleCases {
		r := rulesByKey[ruleOf(name)]
		t.Run(name, func(t *testing.T) {
			p := planYAML(t, tc.src)
			want := Finding{Service: tc.service, Key: tc.key, Message: r.Message, Hint: r.Hint}
			in, other := p.Warnings, p.Refusals
			if r.Refuse {
				in, other = p.Refusals, p.Warnings
			}
			if !slices.Contains(in, want) {
				t.Fatalf("want %s\nrefusals %v\nwarnings %v", want, p.Refusals, p.Warnings)
			}
			if slices.Contains(other, want) {
				t.Fatalf("%s is both refused and warned about", want)
			}
		})
	}
	for _, r := range Rules {
		if _, ok := ruleCases[r.Key]; !ok {
			t.Errorf("no case for rule %q", r.Key)
		}
	}
	for key := range ruleCases {
		if _, ok := rulesByKey[ruleOf(key)]; !ok {
			t.Errorf("case for rule %q, which does not exist", key)
		}
	}
}

func TestUnknownKeysAreRefused(t *testing.T) {
	p := planYAML(t, "services:\n  a:\n    image: alpine\n    user: \"1000\"\n    cap_add: [NET_ADMIN]\nnetworks:\n  default:\n    enable_ipv6: true\n")
	var got []string
	for _, f := range p.Refusals {
		got = append(got, f.Service+"/"+f.Key)
		if !strings.Contains(f.Message, "does not convert "+f.Key) {
			t.Errorf("%s: message %q", f.Key, f.Message)
		}
	}
	want := []string{"/networks.default.enable_ipv6", "a/cap_add", "a/user"}
	if !slices.Equal(got, want) {
		t.Fatalf("refused %v, want %v", got, want)
	}
}

func TestExternalSecretsAndConfigsAreReferenced(t *testing.T) {
	p := planYAML(t, "services:\n  a:\n    image: alpine\n    secrets:\n      - source: s\n        target: token\n        mode: 0o400\n    configs: [c]\nsecrets:\n  s:\n    external: true\n    name: prod-token\nconfigs:\n  c:\n    external: true\n")
	if len(p.Refusals) != 0 {
		t.Fatalf("refusals %v", p.Refusals)
	}
	cs := p.Result.Services[0].TaskTemplate.ContainerSpec
	if len(cs.Secrets) != 1 || cs.Secrets[0].SecretName != "prod-token" || cs.Secrets[0].File.Name != "token" ||
		cs.Secrets[0].File.Mode != 0o400 || cs.Secrets[0].File.UID != "0" {
		t.Fatalf("secrets %+v", cs.Secrets)
	}
	if len(cs.Configs) != 1 || cs.Configs[0].ConfigName != "c" || cs.Configs[0].File.Name != "c" || cs.Configs[0].File.Mode != 0o444 {
		t.Fatalf("configs %+v", cs.Configs)
	}
}

func TestBadValueIsARefusal(t *testing.T) {
	p := planYAML(t, "services:\n  a:\n    image: alpine\n    ports: [\"8080-8081:80\"]\n")
	if p.Result != nil || len(p.Refusals) != 1 || !strings.Contains(p.Refusals[0].Message, "not a single port") {
		t.Fatalf("plan %+v", p)
	}
}

func TestBuildWithoutImageRefusedOnce(t *testing.T) {
	p := planYAML(t, "services:\n  a:\n    build: .\n")
	if len(p.Refusals) != 1 || p.Refusals[0].Message != rulesByKey["build without image"].Message {
		t.Fatalf("refusals %v", p.Refusals)
	}
}

// The lab fixtures must stay deployable to a Swarm.
func TestFixturesHaveNoRefusals(t *testing.T) {
	cases, err := swarmgolden.LoadAll(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		project := loadCaseProject(t, c)
		if p := NewPlan(project, nil, c.Stack()); len(p.Refusals) != 0 {
			t.Errorf("%s: refusals %v", c.Name, p.Refusals)
		}
	}
}
