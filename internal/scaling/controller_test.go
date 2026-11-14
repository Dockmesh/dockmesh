package scaling

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/moby/moby/api/types/container"
)

type fakeTarget struct{ replicas int }

func (f *fakeTarget) MetricsHost() string    { return "agent-1" }
func (f *fakeTarget) LockKey() stacklock.Key { return stacklock.HostKey("agent-1", "app") }
func (f *fakeTarget) Replicas(context.Context) ([]Replica, error) {
	out := make([]Replica, f.replicas)
	for i := range out {
		out[i] = Replica{Name: fmt.Sprintf("app-web-%d", i+1)}
	}
	return out, nil
}
func (f *fakeTarget) Scale(_ context.Context, _, _ string, n int) (*compose.ScaleResult, error) {
	f.replicas = n
	return &compose.ScaleResult{Current: n}, nil
}

// fakeSamples returns one sample per container with the given CPU and
// memory share, and records which containers were queried on which host.
type fakeSamples struct {
	cpu, mem float64
	queried  map[string]bool
}

func (f *fakeSamples) Query(_ context.Context, q metrics.Query) ([]metrics.Sample, error) {
	if f.queried == nil {
		f.queried = map[string]bool{}
	}
	f.queried[q.HostID+"/"+q.Name] = true
	return []metrics.Sample{{CPUPercent: f.cpu, CPUPercentCore: f.cpu, MemUsed: uint64(f.mem), MemLimit: 100}}, nil
}

func testController(target *fakeTarget, samples *fakeSamples) (*Controller, *[]int) {
	var scaled []int
	c := &Controller{
		metrics: samples,
		targets: func(context.Context, string, string) (Target, error) { return target, nil },
		scaleFn: func(ctx context.Context, _ string, t Target, from, to int) error {
			scaled = append(scaled, to)
			_, err := t.Scale(ctx, "", "", to)
			return err
		},
		states: map[string]*ruleState{},
	}
	return c, &scaled
}

func TestScaleDecision(t *testing.T) {
	rule := func(downMetric string) Rule {
		return Rule{
			Service: "web", MinReplicas: 1, MaxReplicas: 5, CPUUnit: CPUUnitHost,
			ScaleUp:   ThresholdConfig{Metric: "cpu", ThresholdPercent: 80},
			ScaleDown: ThresholdConfig{Metric: downMetric, ThresholdPercent: 20},
		}
	}
	tests := []struct {
		name     string
		rule     Rule
		replicas int
		cpu, mem float64
		want     []int
	}{
		{name: "scale down on its own metric", rule: rule("memory"), replicas: 3, cpu: 50, mem: 10, want: []int{2}},
		{name: "own metric above threshold", rule: rule("memory"), replicas: 3, cpu: 5, mem: 50},
		{name: "no scale-down metric uses scale-up metric", rule: rule(""), replicas: 3, cpu: 5, mem: 90, want: []int{2}},
		{name: "scale up from the running count", rule: rule("cpu"), replicas: 3, cpu: 95, want: []int{4}},
		{name: "not running", rule: rule("cpu"), replicas: 0, cpu: 95},
		{name: "at max", rule: rule("cpu"), replicas: 5, cpu: 95},
		{name: "at min", rule: rule("cpu"), replicas: 1, cpu: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, scaled := testController(&fakeTarget{replicas: tt.replicas}, &fakeSamples{cpu: tt.cpu, mem: tt.mem})
			c.evaluateRule(context.Background(), "app", tt.rule)
			if fmt.Sprint(*scaled) != fmt.Sprint(tt.want) {
				t.Fatalf("scaled to %v, want %v", *scaled, tt.want)
			}
		})
	}
}

func TestEverySampledReplica(t *testing.T) {
	samples := &fakeSamples{cpu: 1}
	c, _ := testController(&fakeTarget{replicas: 25}, samples)
	c.evaluateRule(context.Background(), "app", Rule{Service: "web", MinReplicas: 1, MaxReplicas: 30,
		ScaleUp: ThresholdConfig{Metric: "cpu", ThresholdPercent: 80}})
	if len(samples.queried) != 25 {
		t.Fatalf("queried %d replicas, want 25", len(samples.queried))
	}
	if !samples.queried["agent-1/app-web-25"] {
		t.Fatal("replica 25 not queried on the target's host")
	}
}

// A new controller, as after a restart, starts from the replicas that
// run.
func TestRestartKeepsCount(t *testing.T) {
	target := &fakeTarget{replicas: 3}
	rule := Rule{Service: "web", MinReplicas: 1, MaxReplicas: 5,
		ScaleUp: ThresholdConfig{Metric: "cpu", ThresholdPercent: 80}, CooldownSeconds: 60}
	c, _ := testController(target, &fakeSamples{cpu: 95})
	c.evaluateRule(context.Background(), "app", rule)
	if target.replicas != 4 {
		t.Fatalf("replicas = %d, want 4", target.replicas)
	}
	restarted, scaled := testController(target, &fakeSamples{cpu: 95})
	restarted.evaluateRule(context.Background(), "app", rule)
	if fmt.Sprint(*scaled) != "[5]" {
		t.Fatalf("after restart scaled to %v, want [5]", *scaled)
	}
}

func TestThresholdHeldPerDirection(t *testing.T) {
	c, scaled := testController(&fakeTarget{replicas: 3}, &fakeSamples{cpu: 95})
	rule := Rule{Service: "web", MinReplicas: 1, MaxReplicas: 5,
		ScaleUp:   ThresholdConfig{Metric: "cpu", ThresholdPercent: 80, DurationSeconds: 60},
		ScaleDown: ThresholdConfig{Metric: "cpu", ThresholdPercent: 20, DurationSeconds: 60}}
	c.evaluateRule(context.Background(), "app", rule)
	st := c.states["app/web"]
	st.exceededSince = time.Now().Add(-2 * time.Minute)
	c.metrics = &fakeSamples{cpu: 5}
	c.evaluateRule(context.Background(), "app", rule)
	if len(*scaled) != 0 {
		t.Fatalf("scaled to %v right after the scale-up threshold was crossed", *scaled)
	}
}

type listHost struct {
	host.Host
	list []container.Summary
}

func (h listHost) ID() string { return "agent-1" }
func (h listHost) ListContainers(context.Context, bool) ([]container.Summary, error) {
	return h.list, nil
}

func TestHostTargetReplicasByLabel(t *testing.T) {
	web := map[string]string{compose.LabelProject: "app", compose.LabelService: "web"}
	h := listHost{list: []container.Summary{
		{ID: "a", Names: []string{"/app-web-1"}, Labels: web, State: "running"},
		{ID: "b", Names: []string{"/renamed"}, Labels: web, State: "running"},
		{ID: "c", Names: []string{"/app-web-3"}, Labels: web, State: "exited"},
		{ID: "d", Names: []string{"/app-web-4"}, Labels: map[string]string{compose.LabelProject: "app", compose.LabelService: "db"}, State: "running"},
		{ID: "e", Names: []string{"/app-web-5"}, Labels: map[string]string{compose.LabelProject: "other", compose.LabelService: "web"}, State: "running"},
	}}
	got, err := NewHostTarget(h, "app", "web").Replicas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[{app-web-1 a} {renamed b}]" {
		t.Fatalf("replicas = %v", got)
	}
}
