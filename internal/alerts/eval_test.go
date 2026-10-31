package alerts

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
)

var evalNow = time.Unix(1_800_000_000, 0)

// series builds samples every 30s ending at evalNow-lag; values are
// listed oldest first.
func series(lag int64, values ...float64) []metricSample {
	out := make([]metricSample, len(values))
	end := evalNow.Unix() - lag
	for i, v := range values {
		out[i] = metricSample{TS: end - int64(len(values)-1-i)*30, Value: v}
	}
	return out
}

func TestDecide(t *testing.T) {
	const th = 90.0
	tests := []struct {
		name      string
		samples   []metricSample
		duration  time.Duration
		wasFiring bool
		want      bool
	}{
		{"no samples", nil, time.Minute, false, false},
		{"breach covers 5 min window", series(3, 95, 95, 95, 95, 95, 95, 95, 95, 95, 95), 5 * time.Minute, false, true},
		{"breach only 4 min of 5", series(3, 50, 95, 95, 95, 95, 95, 95, 95, 95), 5 * time.Minute, false, false},
		{"one dip inside window", series(3, 95, 95, 95, 95, 80, 95, 95, 95, 95, 95), 5 * time.Minute, false, false},
		{"one missed sample tolerated", append(series(93, 95, 95, 95, 95, 95, 95, 95), series(3, 95, 95)...), 5 * time.Minute, false, true},
		{"long gap breaks the run", append(series(153, 95, 95, 95, 95, 95, 95), series(3, 95, 95)...), 5 * time.Minute, false, false},
		{"duration zero fires on newest sample", series(3, 50, 95), 0, false, true},
		{"duration zero newest clear", series(3, 95, 50), 0, false, false},
		{"stale series never fires", series(120, 95, 95, 95, 95), 30 * time.Second, false, false},
		{"firing stays on single clear sample", series(3, 95, 95, 95, 50), 5 * time.Minute, true, true},
		{"firing stays on dip then breach", series(3, 95, 50, 95), 5 * time.Minute, true, true},
		{"firing resolves after one clear interval", series(3, 95, 95, 50, 50), 5 * time.Minute, true, false},
		{"firing resolves when stale", series(120, 95, 95), 5 * time.Minute, true, false},
		{"firing keeps firing while breached", series(3, 95, 95), 5 * time.Minute, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decide(tt.samples, "gt", th, tt.duration, tt.wasFiring, evalNow, DefaultSampleInterval)
			if got != tt.want {
				t.Fatalf("decide = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecideLessThan(t *testing.T) {
	if !decide(series(3, 5, 5, 5), "lt", 10, time.Minute, false, evalNow, DefaultSampleInterval) {
		t.Fatal("lt breach over the window should fire")
	}
	if decide(series(3, 5, 15, 5), "lt", 10, time.Minute, false, evalNow, DefaultSampleInterval) {
		t.Fatal("lt with a clear sample inside the window must not fire")
	}
}

func newEvalService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM alert_rules`); err != nil {
		t.Fatal(err)
	}
	return NewService(database, nil), database
}

func addSample(t *testing.T, database *sql.DB, container string, ts int64, cpu float64) {
	t.Helper()
	if _, err := database.Exec(`
		INSERT INTO metrics_raw (container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
		VALUES (?, ?, ?, 0, 0, 0, 0, 0, 0)`, container, ts, cpu); err != nil {
		t.Fatal(err)
	}
}

type historyRow struct {
	status   string
	notified bool
}

func historyOf(t *testing.T, database *sql.DB, ruleID int64) []historyRow {
	t.Helper()
	rows, err := database.Query(`SELECT status, notified FROM alert_history WHERE rule_id = ? ORDER BY id`, ruleID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []historyRow
	for rows.Next() {
		var h historyRow
		var n int
		if err := rows.Scan(&h.status, &n); err != nil {
			t.Fatal(err)
		}
		h.notified = n == 1
		out = append(out, h)
	}
	return out
}

func wantHistory(t *testing.T, database *sql.DB, ruleID int64, want ...historyRow) {
	t.Helper()
	if got := historyOf(t, database, ruleID); !slices.Equal(got, want) {
		t.Fatalf("history = %+v, want %+v", got, want)
	}
}

// run feeds one sample per 30s tick and evaluates two seconds after each.
func run(t *testing.T, s *Service, database *sql.DB, r *Rule, start time.Time, values ...float64) {
	t.Helper()
	now := start
	for _, v := range values {
		addSample(t, database, "web", now.Unix(), v)
		now = now.Add(2 * time.Second)
		rule, err := s.getRule(context.Background(), r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.evalRule(context.Background(), rule, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(28 * time.Second)
	}
}

func createRule(t *testing.T, s *Service, in RuleInput) *Rule {
	t.Helper()
	in.Name = "cpu"
	in.ContainerFilter = "*"
	in.Metric = "cpu_percent"
	in.Operator = "gt"
	in.Threshold = 90
	in.Enabled = true
	r, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEvalFiresOnceAndResolves(t *testing.T) {
	s, database := newEvalService(t)
	r := createRule(t, s, RuleInput{DurationSeconds: 120, CooldownSeconds: 1})

	run(t, s, database, r, evalNow, 95, 95, 95, 95, 95, 95, 50, 95, 95, 50, 50, 50)
	wantHistory(t, database, r.ID, historyRow{"fired", true}, historyRow{"resolved", true})

	stats, err := s.RuleStatsByID(context.Background(), r.ID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if stats.FiresCount != 1 || stats.LastFired == nil {
		t.Fatalf("stats = %+v, want one fire", stats)
	}
}

func TestEvalCooldownSuppressesRenotify(t *testing.T) {
	s, database := newEvalService(t)
	r := createRule(t, s, RuleInput{DurationSeconds: 60, CooldownSeconds: 3600})

	run(t, s, database, r, evalNow, 95, 95, 95, 50, 50, 50, 95, 95, 95, 50, 50, 50)
	wantHistory(t, database, r.ID, historyRow{"fired", true}, historyRow{"resolved", true}, historyRow{"fired", false}, historyRow{"resolved", false})
}

func TestEvalMutedUntilSuppressesFireAndResolve(t *testing.T) {
	s, database := newEvalService(t)
	until := evalNow.Add(time.Hour).UTC().Format(time.RFC3339)
	r := createRule(t, s, RuleInput{DurationSeconds: 60, MutedUntil: until})

	run(t, s, database, r, evalNow, 95, 95, 95, 50, 50, 50)
	wantHistory(t, database, r.ID, historyRow{"fired", false}, historyRow{"resolved", false})
}

func TestEvalResolveRespectsMuteSchedule(t *testing.T) {
	s, database := newEvalService(t)
	r := createRule(t, s, RuleInput{DurationSeconds: 60})
	run(t, s, database, r, evalNow, 95, 95, 95)

	// Mute every day, all day, then let the breach clear.
	if _, err := database.Exec(`UPDATE alert_rules SET mute_schedule = ? WHERE id = ?`,
		`{"type":"weekly","ranges":[{"days":[],"from":"00:00","to":"24:00"}]}`, r.ID); err != nil {
		t.Fatal(err)
	}
	run(t, s, database, r, evalNow.Add(90*time.Second), 50, 50, 50)

	wantHistory(t, database, r.ID, historyRow{"fired", true}, historyRow{"resolved", false})
}

func TestLoadFiringStateRestoresCooldown(t *testing.T) {
	s, database := newEvalService(t)
	r := createRule(t, s, RuleInput{DurationSeconds: 60})
	run(t, s, database, r, evalNow, 95, 95, 95)

	restarted := NewService(database, nil)
	if err := restarted.loadFiringState(context.Background()); err != nil {
		t.Fatal(err)
	}
	key := stateKey(r.ID, "web")
	if notified, ok := restarted.firing[key]; !ok || !notified {
		t.Fatalf("firing state after restart = %v,%v; want active and notified", notified, ok)
	}
	if _, ok := restarted.lastNotified[key]; !ok {
		t.Fatal("cooldown timestamp not restored")
	}
}
