package migration

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"

	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/system"
)

const mib = 1024 * 1024

func intp(i int) *int { return &i }

func TestComputeNeeds(t *testing.T) {
	proj := &types.Project{Services: types.Services{
		"db": {Name: "db", Image: "postgres:17", Deploy: &types.DeployConfig{Resources: types.Resources{
			Reservations: &types.Resource{MemoryBytes: 256 * mib, NanoCPUs: 0.5},
			Limits:       &types.Resource{MemoryBytes: 1024 * mib, NanoCPUs: 2},
		}}},
		"web":    {Name: "web", Image: "nginx:1", MemLimit: 64 * mib, CPUS: 0.25, Scale: intp(2)},
		"worker": {Name: "worker", Image: "app:1"},
		"cron":   {Name: "cron", Image: "app:1"},
	}}
	usage := map[string]serviceUsage{
		"web":    {containers: 3, sampled: 3, mem: 999 * mib},
		"worker": {containers: 2, sampled: 2, mem: 100 * mib, cpus: 0.3},
		"cron":   {containers: 1, sampled: 0},
	}
	src := []image.Summary{{RepoTags: []string{"postgres:17"}, Size: 400 * mib}, {RepoTags: []string{"nginx:1"}, Size: 50 * mib}}
	tgt := []image.Summary{{RepoTags: []string{"nginx:1"}}}

	got := computeNeeds(proj, usage, src, tgt)
	want := stackNeeds{
		// db: reservation; web: limit × 3 running containers; worker: measured.
		MemBytes:    256*mib + 3*64*mib + 100*mib,
		CPUs:        0.5 + 3*0.25 + 0.3,
		DiskBytes:   400 * mib,
		MemUnknown:  []string{"cron"},
		CPUUnknown:  []string{"cron"},
		DiskUnknown: []string{"app:1"},
		Measured:    true,
	}
	if got.MemBytes != want.MemBytes || got.DiskBytes != want.DiskBytes || !reflect.DeepEqual(got.MemUnknown, want.MemUnknown) ||
		!reflect.DeepEqual(got.CPUUnknown, want.CPUUnknown) || !reflect.DeepEqual(got.DiskUnknown, want.DiskUnknown) || !got.Measured ||
		got.CPUs < want.CPUs-0.001 || got.CPUs > want.CPUs+0.001 {
		t.Fatalf("computeNeeds =\n%+v\nwant\n%+v", got, want)
	}
}

func TestComputeNeedsStoppedStackUsesScale(t *testing.T) {
	proj := &types.Project{Services: types.Services{
		"web": {Name: "web", Image: "nginx:1", MemReservation: 32 * mib, Deploy: &types.DeployConfig{Replicas: intp(4)}},
	}}
	got := computeNeeds(proj, nil, nil, []image.Summary{{RepoTags: []string{"nginx:1"}}})
	if got.MemBytes != 4*32*mib || got.DiskBytes != 0 || len(got.DiskUnknown) != 0 {
		t.Fatalf("computeNeeds = %+v", got)
	}
}

func TestCapacityChecks(t *testing.T) {
	tgt := system.Metrics{MemTotal: 1000 * mib, MemUsed: 600 * mib, CPUCores: 4, CPUUsed: 3, DiskTotal: 10000 * mib, DiskUsed: 9000 * mib}
	cases := []struct {
		name  string
		needs stackNeeds
		tgt   system.Metrics
		want  map[string]string
	}{
		{"fits", stackNeeds{MemBytes: 300 * mib, CPUs: 0.5, DiskBytes: 500 * mib}, tgt,
			map[string]string{"target_capacity": CheckPass, "target_cpu": CheckPass, "target_disk": CheckPass}},
		{"memory with headroom too much", stackNeeds{MemBytes: 350 * mib, CPUs: 0.5}, tgt,
			map[string]string{"target_capacity": CheckFail, "target_cpu": CheckPass, "target_disk": CheckPass}},
		{"cpu and disk too much", stackNeeds{MemBytes: 10 * mib, CPUs: 1.5, DiskBytes: 950 * mib}, tgt,
			map[string]string{"target_capacity": CheckPass, "target_cpu": CheckFail, "target_disk": CheckFail}},
		{"nothing known", stackNeeds{MemUnknown: []string{"web"}, CPUUnknown: []string{"web"}, DiskUnknown: []string{"x:1"}}, tgt,
			map[string]string{"target_capacity": CheckUnknown, "target_cpu": CheckUnknown, "target_disk": CheckUnknown}},
		{"target cores unknown", stackNeeds{CPUs: 1}, system.Metrics{MemTotal: 1000 * mib},
			map[string]string{"target_capacity": CheckPass, "target_cpu": CheckUnknown, "target_disk": CheckPass}},
		{"used above total", stackNeeds{MemBytes: 1}, system.Metrics{MemTotal: 1, MemUsed: 5, CPUCores: 1},
			map[string]string{"target_capacity": CheckFail, "target_cpu": CheckPass, "target_disk": CheckPass}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := map[string]string{}
			for _, ch := range capacityChecks(c.needs, c.tgt) {
				got[ch.Name] = ch.Status
				if ch.Passed != (ch.Status != CheckFail) {
					t.Errorf("%s: passed %v with status %s", ch.Name, ch.Passed, ch.Status)
				}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

type fakeUsage []metrics.Latest

func (f fakeUsage) LatestPerContainer(context.Context, string, int64, int) ([]metrics.Latest, error) {
	return f, nil
}

func TestMeasuredUsage(t *testing.T) {
	s := &Service{usage: fakeUsage{
		{Name: "web-web-1", MemUsed: 10 * mib, CPUPercentCore: 50},
		{Name: "web-web-2", MemUsed: 20 * mib, CPUPercentCore: 25},
		{Name: "other-web-1", MemUsed: 500 * mib},
	}}
	lbl := func(project, svc string) map[string]string {
		return map[string]string{"com.docker.compose.project": project, "com.docker.compose.service": svc}
	}
	list := []container.Summary{
		{Names: []string{"/web-web-1"}, Labels: lbl("web", "web"), State: "running"},
		{Names: []string{"/web-web-2"}, Labels: lbl("web", "web"), State: "running"},
		{Names: []string{"/web-db-1"}, Labels: lbl("web", "db"), State: "running"},
		{Names: []string{"/web-old-1"}, Labels: lbl("web", "old"), State: "exited"},
		{Names: []string{"/other-web-1"}, Labels: lbl("other", "web"), State: "running"},
	}
	got := s.measuredUsage(context.Background(), "local", "web", list, 4)
	want := map[string]serviceUsage{
		"web": {containers: 2, sampled: 2, mem: 30 * mib, cpus: 0.75},
		"db":  {containers: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("measuredUsage = %+v, want %+v", got, want)
	}
}

func TestComputeNeedsSaturates(t *testing.T) {
	huge := types.UnitBytes(1 << 62)
	proj := &types.Project{Services: types.Services{
		"a": {Name: "a", MemLimit: huge, Scale: intp(8)},
		"b": {Name: "b", MemLimit: huge, CPUS: -4},
	}}
	got := computeNeeds(proj, nil, nil, nil)
	if got.MemBytes != math.MaxUint64 {
		t.Fatalf("MemBytes = %d, want saturated", got.MemBytes)
	}
	if got.CPUs != 0 || !reflect.DeepEqual(got.CPUUnknown, []string{"a", "b"}) {
		t.Fatalf("negative cpus counted: %+v", got)
	}
}
