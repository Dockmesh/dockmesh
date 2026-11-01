package agents

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMetricsBatchSanitize(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := now.Unix()
	tests := []struct {
		name      string
		in        MetricsBatch
		host, ctr int
	}{
		{"current samples kept", MetricsBatch{
			Host:       []MetricsHostSample{{TS: ts}, {TS: ts - 1800}},
			Containers: []MetricsContainerSample{{TS: ts, Name: "web"}},
		}, 2, 1},
		{"too old or in the future dropped", MetricsBatch{
			Host:       []MetricsHostSample{{TS: ts - 3*3600}, {TS: ts + 3600}},
			Containers: []MetricsContainerSample{{TS: ts - 3*3600, Name: "web"}, {TS: ts + 3600, Name: "web"}},
		}, 0, 0},
		{"names empty or overlong dropped", MetricsBatch{
			Containers: []MetricsContainerSample{{TS: ts}, {TS: ts, Name: strings.Repeat("x", 256)}, {TS: ts, Name: "ok"}},
		}, 0, 1},
		{"counts capped", MetricsBatch{
			Host:       make([]MetricsHostSample, MaxMetricsHostRows+5),
			Containers: make([]MetricsContainerSample, MaxMetricsContainers+5),
		}, MaxMetricsHostRows, MaxMetricsContainers},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.in
			if tt.name == "counts capped" {
				for i := range b.Host {
					b.Host[i].TS = ts
				}
				for i := range b.Containers {
					b.Containers[i] = MetricsContainerSample{TS: ts, Name: "c"}
				}
			}
			b.Sanitize(now)
			if len(b.Host) != tt.host || len(b.Containers) != tt.ctr {
				t.Errorf("kept %d host / %d container samples, want %d / %d", len(b.Host), len(b.Containers), tt.host, tt.ctr)
			}
		})
	}
}

// Samples are filed under the sending agent's id, and the server offers
// history only when it can store it.
func TestHandleMetricsUsesAgentID(t *testing.T) {
	s := &Service{}
	if slices.Contains(s.capabilities(), CapMetricsHistory) {
		t.Error("history offered without a sink")
	}
	var gotID string
	var got MetricsBatch
	s.SetMetricsSink(func(_ context.Context, id string, b MetricsBatch) error {
		gotID, got = id, b
		return nil
	})
	if !slices.Contains(s.capabilities(), CapMetricsHistory) {
		t.Error("history not offered with a sink")
	}
	payload, err := json.Marshal(map[string]any{
		"host_id":    "local",
		"host":       []MetricsHostSample{{TS: time.Now().Unix(), CPUPercent: 5}},
		"containers": []MetricsContainerSample{{TS: time.Now().Unix(), Name: "web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.handleMetrics(context.Background(), &ConnectedAgent{ID: "agent-1", Name: "a1"}, payload)
	if gotID != "agent-1" || len(got.Host) != 1 || len(got.Containers) != 1 {
		t.Errorf("sink got id %q with %d/%d samples, want agent-1 with 1/1", gotID, len(got.Host), len(got.Containers))
	}
}
