package agents

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
)

func TestSanitizeSwarmServices(t *testing.T) {
	two := uint64(2)
	in := SwarmServicesResp{Services: []SwarmService{
		{ID: "s2", Name: "b", Mode: "global", Replicas: &two},
		{ID: "s1", Name: "a\x00\nx", Mode: "replicated", Replicas: &two, UpdateState: "rollback_started"},
		{ID: "s1", Name: "dup", Mode: "replicated"},
		{ID: "S!", Name: "bad"},
		{ID: "s3", Name: "", Mode: "replicated"},
		{ID: "s4", Name: "c", Mode: "weird"},
	}}
	got := SanitizeSwarmServices(in).Services
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].ID != "s1" || got[0].Name != "ax" || got[0].Replicas == nil || got[0].UpdateState != "rollback_started" {
		t.Fatalf("first %+v", got[0])
	}
	if got[1].Replicas != nil {
		t.Fatal("a global service kept replicas")
	}
	if got[2].Mode != "" {
		t.Fatalf("unknown mode kept: %q", got[2].Mode)
	}

	many := SwarmServicesResp{}
	for i := range maxSwarmServices + 5 {
		many.Services = append(many.Services, SwarmService{ID: fmt.Sprintf("s%d", i), Name: fmt.Sprintf("n%d", i)})
	}
	if r := SanitizeSwarmServices(many); len(r.Services) != maxSwarmServices || !r.Truncated {
		t.Fatalf("%d services, truncated %v", len(r.Services), r.Truncated)
	}
}

func TestSanitizeSwarmServiceDetail(t *testing.T) {
	keys := []string{"A=secret", "B"}
	for i := range maxEnvKeys {
		keys = append(keys, fmt.Sprintf("K%d", i))
	}
	lbl := map[string]string{"big": strings.Repeat("v", 5000)}
	for i := range maxLabels + 10 {
		lbl[fmt.Sprintf("l%03d", i)] = "x"
	}
	d := SanitizeSwarmServiceDetail(SwarmServiceDetail{
		SwarmService: SwarmService{ID: "s1", Name: "web"},
		EnvKeys:      keys,
		Labels:       lbl,
		Secrets:      []SwarmRef{{ID: "x!", Name: "pw"}},
		Mounts:       make([]SwarmMount, maxListItems+3),
	})
	if len(d.EnvKeys) != maxEnvKeys || d.EnvKeys[0] != "A" {
		t.Fatalf("env keys %d %v", len(d.EnvKeys), d.EnvKeys[:2])
	}
	if len(d.Labels) != maxLabels {
		t.Fatalf("%d labels", len(d.Labels))
	}
	if v, ok := d.Labels["big"]; ok && len(v) > maxLabelValueLen {
		t.Fatalf("label value of %d bytes", len(v))
	}
	if d.Secrets[0].ID != "" || d.Secrets[0].Name != "pw" {
		t.Fatalf("secret ref %+v", d.Secrets[0])
	}
	if len(d.Mounts) != maxListItems {
		t.Fatalf("%d mounts", len(d.Mounts))
	}
	if SanitizeSwarmServiceDetail(SwarmServiceDetail{SwarmService: SwarmService{ID: "bad id", Name: "x"}}).ID != "" {
		t.Fatal("bad id kept")
	}
}

func TestSanitizeSwarmTasksNewestFirst(t *testing.T) {
	now := time.Now()
	got := SanitizeSwarmTasks(SwarmTasksResp{Tasks: []SwarmTask{
		{ID: "old", ServiceID: "s", UpdatedAt: now.Add(-time.Hour)},
		{ID: "new", ServiceID: "s", UpdatedAt: now},
		{ID: "orphan"},
	}}).Tasks
	if len(got) != 2 || got[0].ID != "new" {
		t.Fatalf("got %+v", got)
	}
}

func TestText(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 5, "abc"},
		{"a\tb\x7f", 5, "ab"},
		{"äöü", 3, "ä"},
		{"ok\xff", 5, "ok"},
	}
	for _, tt := range tests {
		if got := text(tt.in, tt.max); got != tt.want {
			t.Errorf("text(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}

func TestMaskCredentials(t *testing.T) {
	tests := []struct{ in, want string }{
		{"mount failed: password=hunter2,user=x", "mount failed: password=***,user=x"},
		{`token: "a b c" end`, `token: *** end`},
		{"pull https://bob:hunter2@reg.example/x failed", "pull https://***@reg.example/x failed"},
		{"no such image: nginx:1", "no such image: nginx:1"},
	}
	for _, tt := range tests {
		if got := maskCredentials(tt.in); got != tt.want {
			t.Errorf("maskCredentials(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Escaping can blow a list up beyond its raw size; the answer is cut to
// maxAnswerBytes encoded.
func TestSanitizeSwarmTasksFitsEncoded(t *testing.T) {
	long := strings.Repeat("<&", maxTextLen/2)
	in := SwarmTasksResp{}
	for i := range maxSwarmTasks {
		in.Tasks = append(in.Tasks, SwarmTask{ID: fmt.Sprintf("t%d", i), ServiceID: "s", Message: long, Error: long, Image: long})
	}
	out := SanitizeSwarmTasks(in)
	b, _ := json.Marshal(out)
	if len(b) > maxAnswerBytes+1024 || !out.Truncated || len(out.Tasks) == 0 {
		t.Fatalf("%d bytes, %d tasks, truncated %v", len(b), len(out.Tasks), out.Truncated)
	}
}

func TestSwarmServiceNetworks(t *testing.T) {
	s := swarm.Service{ID: "s1", Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "web"},
		TaskTemplate: swarm.TaskSpec{Networks: []swarm.NetworkAttachmentConfig{{Target: "n1"}, {Target: "app_back"}}}},
		Endpoint: swarm.Endpoint{VirtualIPs: []swarm.EndpointVirtualIP{{NetworkID: "ing"}, {NetworkID: "n1"}}}}
	got := SwarmServicesFrom([]swarm.Service{s}).Services[0].Networks
	if strings.Join(got, ",") != "n1,app_back,ing" {
		t.Fatalf("networks %v", got)
	}
}
