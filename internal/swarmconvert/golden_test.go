package swarmconvert

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/swarmgolden"
)

const casesDir = "../swarmgolden/testdata/cases"

// What each case leaves for the later converter pieces. A case missing
// here must convert completely.
var wantNotConverted = map[string][]string{
	"basic-web":        {"web: deploy.placement", "web: deploy.resources", "web: deploy.update_config"},
	"fixture-app":      {"volumes.data", "app: restart (use deploy.restart_policy)", "app: volumes"},
	"fixture-nginx":    {"web: restart (use deploy.restart_policy)"},
	"fixture-postgres": {"volumes.pgdata", "db: restart (use deploy.restart_policy)", "db: volumes"},
	"restart-policy":   {"compose-restart: restart (use deploy.restart_policy)"},
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
			if got, want := Strings(res.NotConverted), wantNotConverted[c.Name]; !reflect.DeepEqual(got, want) && (len(got) > 0 || len(want) > 0) {
				t.Errorf("not converted:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}
			if len(c.Meta.Pending) > 0 && len(res.NotConverted) == 0 {
				t.Errorf("case.yaml lists pending paths but nothing is reported as not converted")
			}
			var gotNets, wantNets []string
			for _, n := range res.Networks {
				gotNets = append(gotNets, n.Name+" "+n.Driver+" "+n.Labels[LabelNamespace])
			}
			for _, n := range c.Networks {
				wantNets = append(wantNets, n.Name+" "+n.Driver+" "+n.Labels[LabelNamespace])
			}
			sort.Strings(gotNets)
			if !reflect.DeepEqual(gotNets, wantNets) {
				t.Errorf("networks %v, want %v", gotNets, wantNets)
			}
		})
	}
	for name := range wantNotConverted {
		if !seen[name] {
			t.Errorf("wantNotConverted names case %s, which does not exist", name)
		}
	}
}

// Keys compose-go knows but the converter doesn't map are reported, never
// dropped; this covers keys no golden case uses.
func TestNotConvertedKeys(t *testing.T) {
	dir := t.TempDir()
	src := `services:
  app:
    image: alpine:3.24
    user: "1000"
    working_dir: /srv
    stop_grace_period: 20s
    privileged: true
    cap_add: [NET_ADMIN]
    networks: [back]
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
`
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := loadProject(dir, "keys")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(project, "keys")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"configs.conf",
		"networks.back",
		"secrets.token",
		"app: cap_add",
		"app: deploy.endpoint_mode",
		"app: deploy.mode",
		"app: networks",
		"app: ports.host_ip",
		"app: privileged",
		"app: stop_grace_period",
		"app: user",
		"app: working_dir",
	}
	if got := Strings(res.NotConverted); !reflect.DeepEqual(got, want) {
		t.Fatalf("not converted:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if len(res.Networks) != 0 || len(res.Services[0].TaskTemplate.Networks) != 0 {
		t.Fatalf("an explicit network was converted: %+v", res.Networks)
	}
}

func loadProject(dir, stack string) (*types.Project, error) {
	return compose.LoadProject(context.Background(), dir, stack, "")
}
