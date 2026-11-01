package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/system"
)

func newTestCollector(t *testing.T) *Collector {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return NewCollector(database, nil, time.Second, DefaultRetention)
}

func TestContainerSeriesPerHost(t *testing.T) {
	c := newTestCollector(t)
	ctx := context.Background()
	now := time.Now().Unix()
	if err := c.InsertContainer(ctx, LocalHostID, "web", now, docker.NormalizedStats{CPUPercentCore: 200, OnlineCPUs: 4, MemUsed: 10}); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertContainer(ctx, "agent-1", "web", now, docker.NormalizedStats{CPUPercentCore: 50, OnlineCPUs: 2, MemUsed: 99}); err != nil {
		t.Fatal(err)
	}
	// A row from before the core count was stored.
	if _, err := c.db.Exec(`INSERT INTO metrics_raw
		(host_id, container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
		VALUES ('local', 'web', ?, 80, 10, 0, 0, 0, 0, 0)`, now-30); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		host     string
		wantCPU  []float64
		wantCore []float64
		wantMem  uint64
	}{
		{LocalHostID, []float64{10, 50}, []float64{80, 200}, 10},
		{"agent-1", []float64{25}, []float64{50}, 99},
	}
	for _, tt := range tests {
		got, err := c.Query(ctx, Query{HostID: tt.host, Name: "web", Cores: 8})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(tt.wantCPU) {
			t.Fatalf("%s: %d samples, want %d", tt.host, len(got), len(tt.wantCPU))
		}
		for i, s := range got {
			if s.CPUPercent != tt.wantCPU[i] || s.CPUPercentCore != tt.wantCore[i] || s.MemUsed != tt.wantMem {
				t.Errorf("%s[%d] = %+v, want cpu %v core %v mem %d", tt.host, i, s, tt.wantCPU[i], tt.wantCore[i], tt.wantMem)
			}
		}
	}

	latest, err := c.LatestPerContainer(ctx, "agent-1", 60, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 1 || latest[0].MemUsed != 99 || latest[0].CPUPercent != 25 {
		t.Errorf("latest on agent-1 = %+v, want one row with mem 99 and cpu 25", latest)
	}
}

func TestHostSeriesAndDownsample(t *testing.T) {
	c := newTestCollector(t)
	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour).Truncate(time.Minute).Unix()
	for i, cpu := range []float64{10, 30} {
		m := system.Metrics{CPUPercent: cpu, CPUCores: 4, MemUsed: 100, MemTotal: 400, DiskUsed: 5, DiskTotal: 10}
		if err := c.InsertHost(ctx, LocalHostID, old+int64(i)*30, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.InsertContainer(ctx, LocalHostID, "web", old, docker.NormalizedStats{CPUPercentCore: 40, OnlineCPUs: 4, NetRx: 7}); err != nil {
		t.Fatal(err)
	}
	c.downsample(ctx)

	got, err := c.QueryHost(ctx, Query{HostID: LocalHostID, Resolution: "1m", From: time.Unix(old-60, 0), To: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].CPUPercent != 20 || got[0].CPUCores != 4 || got[0].MemTotal != 400 {
		t.Errorf("host 1m = %+v, want one bucket with cpu 20 of 4 cores", got)
	}
	cs, err := c.Query(ctx, Query{HostID: LocalHostID, Name: "web", Resolution: "1m", From: time.Unix(old-60, 0), To: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].CPUPercent != 10 || cs[0].NetRx != 7 {
		t.Errorf("container 1m = %+v, want one bucket with cpu 10 and net_rx 7", cs)
	}
	if _, err := c.QueryHost(ctx, Query{Resolution: "5m"}); err != ErrResolution {
		t.Errorf("bad resolution: err = %v, want ErrResolution", err)
	}

	for host, want := range map[string]bool{LocalHostID: true, "agent-1": false} {
		has, err := c.HasHistory(ctx, host)
		if err != nil {
			t.Fatal(err)
		}
		if has != want {
			t.Errorf("HasHistory(%s) = %v, want %v", host, has, want)
		}
	}
}

func TestStoreAgentBatch(t *testing.T) {
	c := newTestCollector(t)
	ctx := context.Background()
	now := time.Now().Unix()
	err := c.StoreAgentBatch(ctx, "agent-1", agents.MetricsBatch{
		Host:       []agents.MetricsHostSample{{TS: now, CPUPercent: 40, CPUCores: 2, MemTotal: 8}},
		Containers: []agents.MetricsContainerSample{{TS: now, Name: "web", CPUPercentCore: 100, OnlineCPUs: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hs, err := c.QueryHost(ctx, Query{HostID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := c.Query(ctx, Query{HostID: "agent-1", Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0].CPUPercent != 40 || len(cs) != 1 || cs[0].CPUPercent != 50 {
		t.Errorf("agent-1 host %+v containers %+v, want cpu 40 and 50", hs, cs)
	}
	local, err := c.Query(ctx, Query{Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 0 {
		t.Errorf("agent samples leaked into local: %+v", local)
	}
}
