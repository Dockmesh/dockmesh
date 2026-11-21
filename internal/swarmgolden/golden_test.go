package swarmgolden

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"gopkg.in/yaml.v3"
)

// engineVersion is the Docker Engine the cases were recorded on. A new
// engine means recording again and reviewing the diff (README.md).
const engineVersion = "29.9.2"

const casesDir = "testdata/cases"

func TestRecordedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/engine-version")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != engineVersion {
		t.Fatalf("cases recorded on engine %s, tests expect %s", got, engineVersion)
	}
	cases, err := LoadAll(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var project struct {
				Services map[string]any `yaml:"services"`
			}
			if err := yaml.Unmarshal(c.Compose, &project); err != nil {
				t.Fatal(err)
			}
			if len(c.Services) != len(project.Services) {
				t.Fatalf("%d recorded services for %d in compose.yaml: record the case again", len(c.Services), len(project.Services))
			}
			for _, s := range c.Services {
				svc, ok := strings.CutPrefix(s.Name, c.Stack()+"_")
				if !ok || project.Services[svc] == nil {
					t.Errorf("recorded service %s is not a service of compose.yaml", s.Name)
				}
				if s.Labels["com.docker.stack.namespace"] != c.Stack() {
					t.Errorf("%s: namespace label %q", s.Name, s.Labels["com.docker.stack.namespace"])
				}
			}
			var config struct {
				Services map[string]any `yaml:"services"`
			}
			if err := yaml.Unmarshal(c.StackConfig, &config); err != nil {
				t.Fatalf("stack-config.yaml: %v", err)
			}
			if len(config.Services) != len(project.Services) {
				t.Errorf("stack-config.yaml has %d services, compose.yaml %d", len(config.Services), len(project.Services))
			}
			for _, a := range c.Meta.Accepted {
				if a.Reason == "" {
					t.Errorf("accepted difference %s has no reason", a.Path)
				}
			}
			log, err := os.ReadFile(filepath.Join(c.Dir, "recorded", "deploy.log"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(log), "\nexit ") {
				t.Errorf("deploy.log has no exit line")
			}
		})
	}
}

func spec(name string, mut func(*swarm.ServiceSpec)) swarm.ServiceSpec {
	s := swarm.ServiceSpec{Annotations: swarm.Annotations{Name: name, Labels: map[string]string{"com.docker.stack.namespace": "gt-x"}}}
	s.TaskTemplate.ContainerSpec = &swarm.ContainerSpec{Image: "alpine:3.24"}
	s.Mode.Replicated = &swarm.ReplicatedService{}
	if mut != nil {
		mut(&s)
	}
	return s
}

func u64(v uint64) *uint64 { return &v }

func TestCompareServices(t *testing.T) {
	cases := []struct {
		name     string
		want     []swarm.ServiceSpec
		got      []swarm.ServiceSpec
		ignored  []string
		wantDiff []string
	}{
		{
			name: "equal",
			want: []swarm.ServiceSpec{spec("gt-x_a", nil)},
			got:  []swarm.ServiceSpec{spec("gt-x_a", nil)},
		},
		{
			name: "empty values count as missing",
			want: []swarm.ServiceSpec{spec("gt-x_a", func(s *swarm.ServiceSpec) {
				s.TaskTemplate.Resources = &swarm.ResourceRequirements{}
				s.TaskTemplate.Placement = &swarm.Placement{}
				s.EndpointSpec = &swarm.EndpointSpec{}
				s.TaskTemplate.ContainerSpec.Env = []string{}
			})},
			got: []swarm.ServiceSpec{spec("gt-x_a", nil)},
		},
		{
			name:     "replicas 0 differs from unset",
			want:     []swarm.ServiceSpec{spec("gt-x_a", func(s *swarm.ServiceSpec) { s.Mode.Replicated.Replicas = u64(0) })},
			got:      []swarm.ServiceSpec{spec("gt-x_a", nil)},
			wantDiff: []string{"gt-x_a:Mode/Replicated/Replicas: want 0, got (missing)"},
		},
		{
			name:     "label with dots",
			want:     []swarm.ServiceSpec{spec("gt-x_a", func(s *swarm.ServiceSpec) { s.Labels["com.docker.stack.image"] = "alpine:3.24" })},
			got:      []swarm.ServiceSpec{spec("gt-x_a", nil)},
			wantDiff: []string{`gt-x_a:Labels/com.docker.stack.image: want "alpine:3.24", got (missing)`},
		},
		{
			name:    "ignored label",
			want:    []swarm.ServiceSpec{spec("gt-x_a", func(s *swarm.ServiceSpec) { s.Labels["com.docker.stack.image"] = "alpine:3.24" })},
			got:     []swarm.ServiceSpec{spec("gt-x_a", nil)},
			ignored: []string{"gt-x_a:Labels/com.docker.stack.image"},
		},
		{
			name: "ignored for every service",
			want: []swarm.ServiceSpec{spec("gt-x_a", func(s *swarm.ServiceSpec) {
				s.TaskTemplate.Networks = []swarm.NetworkAttachmentConfig{{Target: "gt-x_default"}}
			}), spec("gt-x_b", func(s *swarm.ServiceSpec) {
				s.TaskTemplate.Networks = []swarm.NetworkAttachmentConfig{{Target: "gt-x_default"}}
			})},
			got:     []swarm.ServiceSpec{spec("gt-x_b", nil), spec("gt-x_a", nil)},
			ignored: []string{"*:TaskTemplate/Networks"},
		},
		{
			name:     "ignore of another service does not apply",
			want:     []swarm.ServiceSpec{spec("gt-x_a", func(s *swarm.ServiceSpec) { s.TaskTemplate.ContainerSpec.Env = []string{"A=1"} })},
			got:      []swarm.ServiceSpec{spec("gt-x_a", nil)},
			ignored:  []string{"gt-x_b:TaskTemplate/ContainerSpec/Env"},
			wantDiff: []string{`gt-x_a:TaskTemplate/ContainerSpec/Env: want ["A=1"], got (missing)`},
		},
		{
			name:     "array element",
			want:     []swarm.ServiceSpec{spec("gt-x_a", func(s *swarm.ServiceSpec) { s.TaskTemplate.ContainerSpec.Env = []string{"A=1", "B=2"} })},
			got:      []swarm.ServiceSpec{spec("gt-x_a", func(s *swarm.ServiceSpec) { s.TaskTemplate.ContainerSpec.Env = []string{"A=1", "B=3"} })},
			wantDiff: []string{`gt-x_a:TaskTemplate/ContainerSpec/Env[1]: want "B=2", got "B=3"`},
		},
		{
			name:     "missing and extra service",
			want:     []swarm.ServiceSpec{spec("gt-x_a", nil)},
			got:      []swarm.ServiceSpec{spec("gt-x_b", nil)},
			wantDiff: []string{"gt-x_a:: want service, got (missing)", "gt-x_b:: want (missing), got service"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diffs, err := CompareServices(tc.want, tc.got, tc.ignored)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range diffs {
				got = append(got, d.String())
			}
			if strings.Join(got, "\n") != strings.Join(tc.wantDiff, "\n") {
				t.Fatalf("diffs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.wantDiff, "\n"))
			}
		})
	}
}

func TestCompareServicesRefusesDuplicates(t *testing.T) {
	if _, err := CompareServices([]swarm.ServiceSpec{spec("gt-x_a", nil), spec("gt-x_a", nil)}, nil, nil); err == nil {
		t.Fatal("duplicate service names accepted")
	}
}
