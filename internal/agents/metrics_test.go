package agents

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMetricsBatchSanitize(t *testing.T) {
	now := time.Unix(1_800_000_000, 0) // a multiple of 30 s
	ts := now.Unix()
	many := func(n int, name func(int) string) []MetricsContainerSample {
		out := make([]MetricsContainerSample, n)
		for i := range out {
			out[i] = MetricsContainerSample{TS: ts, Name: name(i)}
		}
		return out
	}
	tests := []struct {
		name      string
		in        MetricsBatch
		host, ctr int
		hostTS    int64
	}{
		{"current samples kept and snapped", MetricsBatch{
			Host:       []MetricsHostSample{{TS: ts + 7}, {TS: ts - 1800}},
			Containers: []MetricsContainerSample{{TS: ts, Name: "web"}},
		}, 2, 1, ts},
		{"too old or in the future dropped", MetricsBatch{
			Host:       []MetricsHostSample{{TS: ts - 3*3600}, {TS: ts + 3600}},
			Containers: []MetricsContainerSample{{TS: ts - 3*3600, Name: "web"}, {TS: ts + 3600, Name: "web"}},
		}, 0, 0, 0},
		{"agent clock 10 min ahead is shifted", MetricsBatch{
			SentAt:     ts + 600,
			Host:       []MetricsHostSample{{TS: ts + 600}},
			Containers: []MetricsContainerSample{{TS: ts + 570, Name: "web"}},
		}, 1, 1, ts},
		{"names Docker wouldn't allow dropped", MetricsBatch{
			Containers: []MetricsContainerSample{{TS: ts}, {TS: ts, Name: strings.Repeat("x", 256)},
				{TS: ts, Name: "a b"}, {TS: ts, Name: "-x"}, {TS: ts, Name: "ok_1.a-b"}},
		}, 0, 1, 0},
		{"rows capped", MetricsBatch{
			Host:       make([]MetricsHostSample, MaxMetricsHostRows+5),
			Containers: many(MaxMetricsContainers+5, func(int) string { return "c" }),
		}, MaxMetricsHostRows, MaxMetricsContainers, 0},
		{"distinct names capped", MetricsBatch{
			Containers: many(maxMetricsNames+5, func(i int) string { return "c" + strconv.Itoa(i) }),
		}, 0, maxMetricsNames, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.in
			for i := range b.Host {
				if b.Host[i].TS == 0 {
					b.Host[i].TS = ts
				}
			}
			b.Sanitize(now)
			if len(b.Host) != tt.host || len(b.Containers) != tt.ctr {
				t.Fatalf("kept %d host / %d container samples, want %d / %d", len(b.Host), len(b.Containers), tt.host, tt.ctr)
			}
			if tt.hostTS != 0 && b.Host[0].TS != tt.hostTS {
				t.Errorf("host ts = %d, want %d", b.Host[0].TS, tt.hostTS)
			}
		})
	}
}

func readAck(t *testing.T, ag *ConnectedAgent) ResponseEnvelope {
	t.Helper()
	select {
	case f := <-ag.send:
		var env ResponseEnvelope
		if err := json.Unmarshal(f.Payload, &env); err != nil {
			t.Fatal(err)
		}
		return env
	case <-time.After(time.Second):
		t.Fatal("no answer sent")
	}
	return ResponseEnvelope{}
}

// Samples are filed under the sending agent's id and acknowledged, and the
// server offers history only when it can store it.
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
	ag := newConnectedAgent("agent-1", "a1", func() {})
	s.handleMetrics(context.Background(), ag, Frame{ID: "f1", Payload: payload})
	if gotID != "agent-1" || len(got.Host) != 1 || len(got.Containers) != 1 {
		t.Errorf("sink got id %q with %d/%d samples, want agent-1 with 1/1", gotID, len(got.Host), len(got.Containers))
	}
	if env := readAck(t, ag); !env.OK {
		t.Errorf("ack = %+v, want ok", env)
	}
}

// A batch sooner than MetricsMinGap after the last one, or while one is
// stored, is refused so the agent keeps it.
func TestAcceptMetricsGap(t *testing.T) {
	ag := newConnectedAgent("agent-1", "a1", func() {})
	now := time.Now()
	if !ag.acceptMetrics(now) {
		t.Fatal("first batch refused")
	}
	ag.ingesting.Store(false)
	if ag.acceptMetrics(now.Add(MetricsMinGap / 2)) {
		t.Error("batch inside the gap accepted")
	}
	if !ag.acceptMetrics(now.Add(MetricsMinGap)) {
		t.Error("batch after the gap refused")
	}
	if ag.acceptMetrics(now.Add(3 * MetricsMinGap)) {
		t.Error("second batch accepted while one is in flight")
	}
	ag.refuseMetrics(context.Background(), Frame{ID: "f2"})
	if env := readAck(t, ag); env.OK {
		t.Errorf("refusal = %+v, want not ok", env)
	}
}
