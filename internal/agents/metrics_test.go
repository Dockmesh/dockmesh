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
		{"skew under one slot is left alone", MetricsBatch{
			SentAt: ts - 3,
			Host:   []MetricsHostSample{{TS: ts + 29}},
		}, 1, 0, ts},
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

// The limits are per agent, not per session: a reconnect doesn't reset
// the gap or the name budget.
func TestMetricsIntakeLimits(t *testing.T) {
	s := &Service{}
	now := time.Now()
	in := s.intake("agent-1")
	if !in.begin(now) {
		t.Fatal("first batch refused")
	}
	if s.intake("agent-1").begin(now.Add(2 * MetricsMinGap)) {
		t.Error("second batch accepted while one is stored")
	}
	in.done()
	if s.intake("agent-1").begin(now.Add(MetricsMinGap / 2)) {
		t.Error("batch inside the gap accepted after a reconnect")
	}
	if !s.intake("agent-1").begin(now.Add(MetricsMinGap)) {
		t.Error("batch after the gap refused")
	}
	if !s.intake("agent-2").begin(now) {
		t.Error("other agent refused")
	}

	batch := func(from, n int) []MetricsContainerSample {
		out := make([]MetricsContainerSample, n)
		for i := range out {
			out[i] = MetricsContainerSample{Name: "c" + strconv.Itoa(from+i)}
		}
		return out
	}
	kept, dropped := in.admit(now, batch(0, MaxMetricsNames-10))
	if len(kept) != MaxMetricsNames-10 || dropped != 0 {
		t.Fatalf("first names: kept %d dropped %d", len(kept), dropped)
	}
	kept, dropped = in.admit(now, append(batch(0, 5), batch(MaxMetricsNames, 20)...))
	if len(kept) != 15 || dropped != 10 {
		t.Errorf("over the budget: kept %d dropped %d, want 15 and 10", len(kept), dropped)
	}
	kept, _ = in.admit(now.Add(metricsNameWindow+time.Minute), batch(5000, 20))
	if len(kept) != 20 {
		t.Errorf("after the window: kept %d new names, want 20", len(kept))
	}
}

func TestRefuseMetricsDoesNotBlock(t *testing.T) {
	ag := newConnectedAgent("agent-1", "a1", func() {})
	for range cap(ag.send) + 5 {
		ag.refuseMetrics(Frame{ID: "f"})
	}
	if env := readAck(t, ag); env.OK {
		t.Errorf("refusal = %+v, want not ok", env)
	}
}
