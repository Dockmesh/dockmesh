package alerts

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func addHostSample(t *testing.T, database *sql.DB, host, container string, ts int64, cpu float64, cores int) {
	t.Helper()
	if _, err := database.Exec(`
		INSERT INTO metrics_raw (host_id, container_name, ts, cpu_percent, cpu_cores, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
		VALUES (?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0)`, host, container, ts, cpu, cores); err != nil {
		t.Fatal(err)
	}
}

type firedRow struct {
	host, target string
}

func firedRows(t *testing.T, database *sql.DB, ruleID int64) []firedRow {
	t.Helper()
	rows, err := database.Query(`SELECT host_id, container_name FROM alert_history WHERE rule_id = ? AND status = 'fired' ORDER BY host_id, container_name`, ruleID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []firedRow
	for rows.Next() {
		var f firedRow
		if err := rows.Scan(&f.host, &f.target); err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// Metric rules cover every host, keep same-named containers apart, follow
// the rule's host scope and compare CPU in the rule's unit.
func TestMetricRulesEvaluateEveryHost(t *testing.T) {
	tests := []struct {
		name string
		rule RuleInput
		want []firedRow
	}{
		{
			name: "all hosts, per core",
			rule: RuleInput{CPUUnit: CPUUnitCore},
			want: []firedRow{{"a1", "edge/web"}, {"local", "web"}},
		},
		{
			name: "host scope",
			rule: RuleInput{CPUUnit: CPUUnitCore, ScopeHost: "a1"},
			want: []firedRow{{"a1", "edge/web"}},
		},
		{
			name: "host tag scope",
			rule: RuleInput{CPUUnit: CPUUnitCore, ScopeTag: "prod"},
			want: []firedRow{{"local", "web"}},
		},
		{
			// 150 per core on 4 cores is 37.5 % of the host on a1; local
			// samples were taken on one core.
			name: "% of host divides by the cores",
			rule: RuleInput{CPUUnit: CPUUnitHost},
			want: []firedRow{{"local", "web"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, database := newEvalService(t)
			s.groupWindow = 0
			captureSends(s)
			s.SetSources(Sources{HostTags: func(id string) []string {
				if id == "local" {
					return []string{"prod"}
				}
				return nil
			}})
			addAgent(t, database, "a1", "edge")
			now := evalNow
			for i := int64(0); i < 4; i++ {
				ts := now.Add(-time.Duration(i) * DefaultSampleInterval).Unix()
				addHostSample(t, database, "local", "web", ts, 150, 1)
				addHostSample(t, database, "a1", "web", ts, 150, 4)
				addHostSample(t, database, "gone-agent", "web", ts, 150, 4)
			}
			in := tt.rule
			in.Name, in.ContainerFilter, in.Metric, in.Operator, in.Threshold = "cpu", "*", "cpu_percent", "gt", 90
			in.DurationSeconds, in.Enabled = 60, true
			r, err := s.Create(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.evalRule(context.Background(), r, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			got := firedRows(t, database, r.ID)
			if len(got) != len(tt.want) {
				t.Fatalf("fired = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("fired = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestCPUUnitDefaultsAndValidation(t *testing.T) {
	s, _ := newEvalService(t)
	ctx := context.Background()
	base := RuleInput{Name: "cpu", ContainerFilter: "*", Metric: "cpu_percent", Operator: "gt", Threshold: 80, Enabled: true}

	r, err := s.Create(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if r.CPUUnit != CPUUnitHost {
		t.Fatalf("new rule unit = %q, want host", r.CPUUnit)
	}

	core := base
	core.CPUUnit = CPUUnitCore
	r, err = s.Create(ctx, core)
	if err != nil {
		t.Fatal(err)
	}
	// A client that doesn't send a unit keeps the stored one.
	r, err = s.Update(ctx, r.ID, base)
	if err != nil {
		t.Fatal(err)
	}
	if r.CPUUnit != CPUUnitCore {
		t.Fatalf("unit after update without one = %q, want core", r.CPUUnit)
	}

	mem := base
	mem.Metric, mem.CPUUnit = "mem_percent", CPUUnitCore
	r, err = s.Create(ctx, mem)
	if err != nil {
		t.Fatal(err)
	}
	if r.CPUUnit != "" {
		t.Fatalf("memory rule unit = %q, want none", r.CPUUnit)
	}

	bad := []RuleInput{base, base}
	bad[0].CPUUnit = "percent"
	bad[1].ScopeStack = "shop"
	for _, in := range bad {
		if _, err := s.Create(ctx, in); err == nil {
			t.Errorf("Create(%+v) accepted", in)
		}
	}
}
