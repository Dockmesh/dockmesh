package swarmconvert

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/swarmgolden"
)

const casesDir = "../swarmgolden/testdata/cases"

// What each case reports as not converted and as warnings; a case
// missing here must convert completely and without warnings.
var wantNotConverted = map[string][]string{
	"fixture-app":      {"app: restart (use deploy.restart_policy)"},
	"fixture-nginx":    {"web: restart (use deploy.restart_policy)"},
	"fixture-postgres": {"db: restart (use deploy.restart_policy)"},
	"networks":         {"networks.unused"},
	"restart-policy":   {"compose-restart: restart (use deploy.restart_policy)"},
}

var wantWarnings = map[string][]string{
	"fixture-app":      {"app: volumes /data: " + localVolume("data")},
	"fixture-postgres": {"db: volumes /var/lib/postgresql/data: " + localVolume("pgdata")},
	"networks":         {"networks.secure: Encrypted overlay traffic between nodes is noticeably slower than unencrypted traffic."},
	"volumes": {
		"app: volumes /data: " + localVolume("data"),
		"app: volumes /etc/node-hostname: " + bindMount("/etc/hostname"),
		"app: volumes /host-log: " + bindMount("/var/log"),
		"app: volumes /outside: External volume outside is looked up on the node each task runs on; it has to exist there.",
		"app: volumes /scratch: Anonymous volume: every task gets a new, empty one.",
		"pinned: volumes /data: " + localVolume("data"),
	},
}

func localVolume(name string) string {
	return "Volume " + name + " is local to each node: a task that moves to another node starts with an empty volume. " +
		"Use shared storage (volume driver options) or pin the service with a placement constraint."
}

func bindMount(src string) string {
	return "Bind mount of " + src + ": the path must exist on every node the service can run on, Swarm does not create it, " +
		"and each node has its own content."
}

func loadCase(t *testing.T, c swarmgolden.Case) *Result {
	t.Helper()
	var env strings.Builder
	for k, v := range c.Env {
		env.WriteString(k + "=" + v + "\n")
	}
	project, err := compose.LoadProject(context.Background(), c.Dir, c.Stack(), env.String())
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(project, c.Stack())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func warningStrings(ws []Warning) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.String()
	}
	return out
}

func TestGoldenCases(t *testing.T) {
	cases, err := swarmgolden.LoadAll(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.Name] = true
		t.Run(c.Name, func(t *testing.T) {
			res := loadCase(t, c)
			diffs, err := swarmgolden.CompareServices(c.Services, res.Services, c.Meta.Ignored())
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range diffs {
				t.Errorf("%s", d)
			}
			if got, want := Strings(res.NotConverted), wantNotConverted[c.Name]; !slices.Equal(got, want) {
				t.Errorf("not converted:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}
			if len(c.Meta.Pending) > 0 && len(res.NotConverted) == 0 {
				t.Errorf("case.yaml lists pending paths but nothing is reported as not converted")
			}
			if got, want := warningStrings(res.Warnings), wantWarnings[c.Name]; !slices.Equal(got, want) {
				t.Errorf("warnings:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}
			netDiffs, err := swarmgolden.CompareNetworks(c.Networks, res.Networks)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range netDiffs {
				t.Errorf("network %s", d)
			}
			gotExt := slices.Sorted(slices.Values(res.ExternalNetworks))
			if want := slices.Sorted(slices.Values(c.ExternalNetworks)); !slices.Equal(gotExt, want) {
				t.Errorf("external networks %v, want %v", gotExt, want)
			}
		})
	}
	for _, m := range []map[string][]string{wantNotConverted, wantWarnings} {
		for name := range m {
			if !seen[name] {
				t.Errorf("case %s does not exist", name)
			}
		}
	}
}

// Keys compose-go knows but the converter doesn't map are reported, never
// dropped; this covers keys no golden case uses.
func TestNotConvertedKeys(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"service keys", `services:
  app:
    image: alpine:3.24
    user: "1000"
    working_dir: /srv
    stop_grace_period: 20s
    privileged: true
    cap_add: [NET_ADMIN]
    networks: [back]
    ulimits:
      nofile: 0
    ports:
      - target: 80
        published: 8080
        host_ip: 127.0.0.1
    deploy:
      mode: global
      endpoint_mode: dnsrr
networks:
  back: {}
secrets:
  token:
    external: true
configs:
  conf:
    external: true
`, []string{
			"configs.conf", "secrets.token",
			"app: cap_add", "app: ports.host_ip",
			"app: privileged", "app: stop_grace_period", "app: ulimits", "app: user", "app: working_dir",
		}},
		{"empty network settings stay default", "services:\n  app:\n    image: alpine\n    networks:\n      default: {}\n", nil},
		{"network attachment settings", "services:\n  app:\n    image: alpine\n    networks:\n      default:\n        aliases: [web]\n        ipv4_address: 198.18.0.5\n        priority: 3\n", []string{"app: networks.ipv4_address", "app: networks.priority"}},
		{"default network with settings", "services:\n  app:\n    image: alpine\nnetworks:\n  default:\n    enable_ipv4: false\n", []string{"networks.default.enable_ipv4"}},
		{"jobs and models", "services:\n  app:\n    image: alpine\njobs:\n  j:\n    image: alpine\n    privileged: true\n    triggers:\n      manual: true\nmodels:\n  m:\n    model: ai/x\n", []string{"jobs.j", "models.m"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := convertYAML(t, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if got := Strings(res.NotConverted); !slices.Equal(got, tc.want) {
				t.Fatalf("not converted:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(tc.want, "\n  "))
			}
			if len(res.Services[0].TaskTemplate.Networks) == 0 {
				t.Fatal("service attached to no network")
			}
		})
	}
}
