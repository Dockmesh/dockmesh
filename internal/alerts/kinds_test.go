package alerts

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/system"
)

func eventRule(t *testing.T, s *Service, in RuleInput) *Rule {
	t.Helper()
	if in.Name == "" {
		in.Name = in.Kind
	}
	in.Enabled = true
	r, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := s.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.setRules(rules)
	return r
}

func statuses(t *testing.T, database *sql.DB, ruleID int64) []string {
	t.Helper()
	var out []string
	for _, h := range historyOf(t, database, ruleID) {
		out = append(out, h.status)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ev(action string, at time.Duration, attrs ...string) ContainerEvent {
	e := ContainerEvent{HostID: "local", Container: "web", Stack: "shop", Action: action, Time: evalNow.Add(at)}
	if len(attrs) > 0 {
		e.ExitCode = attrs[0]
	}
	return e
}

func TestContainerEventRules(t *testing.T) {
	tests := []struct {
		name   string
		rule   RuleInput
		events []ContainerEvent
		want   []string
	}{
		{
			name: "restart loop fires at the third crash in the window",
			rule: RuleInput{Kind: KindRestartLoop, Threshold: 3, DurationSeconds: 600},
			events: []ContainerEvent{
				ev("die", 0, "1"), ev("start", time.Second),
				ev("die", time.Minute, "1"), ev("start", time.Minute+time.Second),
				ev("die", 2*time.Minute, "1"), ev("die", 3*time.Minute, "1"),
			},
			want: []string{"fired"},
		},
		{
			name: "restarts spread beyond the window don't fire",
			rule: RuleInput{Kind: KindRestartLoop, Threshold: 3, DurationSeconds: 600},
			events: []ContainerEvent{
				ev("die", 0, "1"), ev("die", 8*time.Minute, "1"), ev("die", 16*time.Minute, "1"),
			},
		},
		{
			name: "docker stop and restart are not crashes",
			rule: RuleInput{Kind: KindRestartLoop, Threshold: 2, DurationSeconds: 600},
			events: []ContainerEvent{
				ev("kill", 0), ev("die", time.Second, "137"), ev("stop", 2*time.Second),
				ev("kill", 10*time.Second), ev("die", 11*time.Second, "143"),
			},
		},
		{
			name:   "unexpected stop fires and resolves on start",
			rule:   RuleInput{Kind: KindContainerStopped},
			events: []ContainerEvent{ev("die", 0, "1"), ev("start", time.Minute)},
			want:   []string{"fired", "resolved"},
		},
		{
			name:   "clean exit is not unexpected",
			rule:   RuleInput{Kind: KindContainerStopped},
			events: []ContainerEvent{ev("die", 0, "0")},
		},
		{
			name: "unhealthy fires once and resolves",
			rule: RuleInput{Kind: KindUnhealthy},
			events: []ContainerEvent{
				ev("health_status: unhealthy", 0), ev("health_status: unhealthy", time.Minute),
				ev("health_status: healthy", 2*time.Minute),
			},
			want: []string{"fired", "resolved"},
		},
		{
			name:   "oom is one-shot",
			rule:   RuleInput{Kind: KindOOM},
			events: []ContainerEvent{ev("oom", 0), ev("die", time.Second, "137")},
			want:   []string{"fired"},
		},
		{
			name:   "stack scope excludes other stacks",
			rule:   RuleInput{Kind: KindOOM, ScopeStack: "other"},
			events: []ContainerEvent{ev("oom", 0)},
		},
		{
			name:   "host scope excludes other hosts",
			rule:   RuleInput{Kind: KindOOM, ScopeHost: "agent-1"},
			events: []ContainerEvent{ev("oom", 0)},
		},
		{
			name:   "container filter matches by name",
			rule:   RuleInput{Kind: KindOOM, ContainerFilter: "db"},
			events: []ContainerEvent{ev("oom", 0)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, database := newEvalService(t)
			r := eventRule(t, s, tt.rule)
			for _, e := range tt.events {
				s.handleEvent(context.Background(), e)
			}
			if got := statuses(t, database, r.ID); !equalStrings(got, tt.want) {
				t.Fatalf("history = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestartLoopResolvesWhenQuiet(t *testing.T) {
	s, database := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindRestartLoop, Threshold: 2, DurationSeconds: 300})
	s.handleEvent(context.Background(), ev("die", 0, "1"))
	s.handleEvent(context.Background(), ev("die", time.Minute, "1"))

	s.expireRestartLoops(context.Background(), r, evalNow.Add(3*time.Minute))
	s.expireRestartLoops(context.Background(), r, evalNow.Add(7*time.Minute))
	if got := statuses(t, database, r.ID); !equalStrings(got, []string{"fired", "resolved"}) {
		t.Fatalf("history = %v", got)
	}
}

func TestScopeByHostTag(t *testing.T) {
	s, database := newEvalService(t)
	s.SetSources(Sources{HostTags: func(id string) []string {
		if id == "agent-1" {
			return []string{"prod"}
		}
		return nil
	}})
	r := eventRule(t, s, RuleInput{Kind: KindOOM, ScopeTag: "prod"})
	s.handleEvent(context.Background(), ContainerEvent{HostID: "local", Container: "web", Action: "oom", Time: evalNow})
	s.handleEvent(context.Background(), ContainerEvent{HostID: "agent-1", HostName: "edge", Container: "web", Action: "oom", Time: evalNow})

	rows, err := database.Query(`SELECT container_name FROM alert_history WHERE rule_id = ?`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		got = append(got, n)
	}
	if !equalStrings(got, []string{"edge/web"}) {
		t.Fatalf("targets = %v, want [edge/web]", got)
	}
}

func TestHostOffline(t *testing.T) {
	s, database := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindHostOffline, DurationSeconds: 300})
	seen := evalNow.Add(-10 * time.Minute)
	if _, err := database.Exec(`INSERT INTO agents (id, name, status, last_seen_at) VALUES ('a1', 'edge', 'offline', ?), ('a2', 'blip', 'offline', ?)`,
		seen, evalNow.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.evalHostOffline(ctx, r, evalNow); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agents SET status = 'online' WHERE id = 'a1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.evalHostOffline(ctx, r, evalNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := statuses(t, database, r.ID); !equalStrings(got, []string{"fired", "resolved"}) {
		t.Fatalf("history = %v", got)
	}
}

func TestDiskUsage(t *testing.T) {
	s, database := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindDiskUsage, Threshold: 90})
	snapshot := func(containerdPct, homePct float64) []HostMounts {
		return []HostMounts{{HostID: "local", HostName: "local", Mounts: []system.Mount{
			{MountPoint: "/var/lib/containerd", Total: 100, Percent: containerdPct, Roles: []string{system.RoleContainerdRoot}},
			{MountPoint: "/home", Total: 100, Percent: homePct},
		}}}
	}
	ctx := context.Background()
	s.evalDisk(ctx, r, snapshot(95, 99), evalNow)
	s.evalDisk(ctx, r, snapshot(89, 99), evalNow.Add(time.Minute))
	s.evalDisk(ctx, r, snapshot(80, 99), evalNow.Add(2*time.Minute))
	if got := statuses(t, database, r.ID); !equalStrings(got, []string{"fired", "resolved"}) {
		t.Fatalf("history = %v, want fire on the containerd mount only and resolve below the hysteresis", got)
	}
}

func TestBackupFailedOnlyAnnouncesNewRuns(t *testing.T) {
	s, database := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindBackupFailed})
	insert := func(status string) {
		if _, err := database.Exec(`INSERT INTO backup_runs (job_id, job_name, status, error) VALUES (1, 'nightly', ?, 'disk full')`, status); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	insert("failed")
	runs, err := s.newFailedRuns(ctx)
	if err != nil || len(runs) != 0 {
		t.Fatalf("first read must only set the high-water mark: %v %v", runs, err)
	}
	insert("success")
	insert("failed")
	runs, err = s.newFailedRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.evalBackupFailed(ctx, r, runs, evalNow)
	if got := statuses(t, database, r.ID); !equalStrings(got, []string{"fired"}) {
		t.Fatalf("history = %v", got)
	}
}

func TestBackupOverdue(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		schedule string
		since    time.Time
		want     bool
	}{
		{"0 3 * * *", now.Add(-20 * time.Hour), false},
		{"0 3 * * *", now.Add(-31 * time.Hour), true},
		{"@hourly", now.Add(-50 * time.Minute), false},
		{"@hourly", now.Add(-2 * time.Hour), true},
		{"not a schedule", now.Add(-1000 * time.Hour), false},
	}
	for _, tt := range tests {
		if got, _ := backupOverdue(tt.schedule, tt.since, now); got != tt.want {
			t.Errorf("backupOverdue(%q, %s ago) = %v, want %v", tt.schedule, now.Sub(tt.since), got, tt.want)
		}
	}
}

func TestCertExpiry(t *testing.T) {
	s, database := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindCertExpiry, Threshold: 14})
	set := func(days int) {
		list := []CertStatus{{Domain: "app.example.com", NotAfter: evalNow.Add(time.Duration(days) * 24 * time.Hour)}}
		s.certs.Store(&list)
	}
	ctx := context.Background()
	set(30)
	s.evalCerts(ctx, r, evalNow)
	set(10)
	s.evalCerts(ctx, r, evalNow)
	set(90)
	s.evalCerts(ctx, r, evalNow)
	if got := statuses(t, database, r.ID); !equalStrings(got, []string{"fired", "resolved"}) {
		t.Fatalf("history = %v", got)
	}
}

func TestOneShotNotRestoredAsFiring(t *testing.T) {
	s, database := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindOOM})
	s.handleEvent(context.Background(), ev("oom", 0))

	restarted := NewService(database, nil)
	if err := restarted.loadFiringState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.firing[stateKey(r.ID, "web")]; ok {
		t.Fatal("one-shot alert restored as firing")
	}
	if _, ok := restarted.lastNotified[stateKey(r.ID, "web")]; !ok {
		t.Fatal("cooldown for one-shot alert not restored")
	}
}

func TestDefaultEventRules(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "defaults.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	s := NewService(database, nil)
	rules, err := s.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		KindRestartLoop: true, KindOOM: true, KindContainerStopped: false, KindUnhealthy: false,
		KindHostOffline: true, KindDiskUsage: true, KindBackupFailed: true, KindBackupMissed: true,
		KindCertExpiry: true,
	}
	got := map[string]bool{}
	metric := 0
	for _, r := range rules {
		if r.Kind == KindMetric {
			metric++
			continue
		}
		if !r.Builtin {
			t.Errorf("default rule %q not builtin", r.Name)
		}
		got[r.Kind] = r.Enabled
	}
	if metric != 4 {
		t.Errorf("metric builtins = %d, want the 4 existing ones untouched", metric)
	}
	for k, enabled := range want {
		if e, ok := got[k]; !ok || e != enabled {
			t.Errorf("default %s: present=%v enabled=%v, want enabled=%v", k, ok, e, enabled)
		}
	}
	var notices int
	if err := database.QueryRow(`SELECT COUNT(*) FROM notices WHERE key = 'alerts.event-rules-added'`).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 0 {
		t.Errorf("fresh install got the upgrade notice")
	}
}

func TestEventRateLimitPerHost(t *testing.T) {
	s, database := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindOOM})
	for i := 0; i < eventsPerSecond+20; i++ {
		s.handleEvent(context.Background(), ContainerEvent{
			HostID: "a1", HostName: "edge", Container: "c" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Action: "oom", Time: evalNow,
		})
	}
	if got := len(statuses(t, database, r.ID)); got != eventsPerSecond {
		t.Fatalf("recorded %d events in one second, want %d", got, eventsPerSecond)
	}
	s.handleEvent(context.Background(), ContainerEvent{HostID: "a1", HostName: "edge", Container: "late", Action: "oom", Time: evalNow.Add(time.Second)})
	if got := len(statuses(t, database, r.ID)); got != eventsPerSecond+1 {
		t.Fatalf("next window not accepted: %d", got)
	}
}

func TestEventFieldsFromAgentsValidated(t *testing.T) {
	tests := []struct {
		name      string
		container string
		exitCode  string
		wantRows  int
		wantMsg   string
	}{
		{"docker name", "web-1.blue_x", "137", 1, "Container web-1.blue_x on edge was killed"},
		{"slack markup in name", "<!channel> <https://evil|reset>", "137", 0, ""},
		{"control character", "web\x1b[2J", "137", 0, ""},
		{"exit code garbage dropped", "db", "1; rm", 1, "Container db on edge was killed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, database := newEvalService(t)
			r := eventRule(t, s, RuleInput{Kind: KindOOM})
			s.handleEvent(context.Background(), ContainerEvent{
				HostID: "a1", HostName: "edge", Container: tt.container, ExitCode: tt.exitCode,
				Action: "oom", Time: evalNow,
			})
			var msgs []string
			rows, err := database.Query(`SELECT message FROM alert_history WHERE rule_id = ?`, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for rows.Next() {
				var m string
				if err := rows.Scan(&m); err != nil {
					t.Fatal(err)
				}
				msgs = append(msgs, m)
			}
			if len(msgs) != tt.wantRows || (tt.wantRows > 0 && !strings.HasPrefix(msgs[0], tt.wantMsg)) {
				t.Fatalf("history = %q, want %d row(s) starting %q", msgs, tt.wantRows, tt.wantMsg)
			}
		})
	}
}

func TestDieHistoryPrunedForQuietContainers(t *testing.T) {
	s, _ := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindRestartLoop, Threshold: 3, DurationSeconds: 300})
	s.handleEvent(context.Background(), ev("die", 0, "0"))
	if len(s.dies) != 1 {
		t.Fatalf("dies = %d, want 1", len(s.dies))
	}
	s.expireRestartLoops(context.Background(), r, evalNow.Add(10*time.Minute))
	if len(s.dies) != 0 {
		t.Fatalf("die history of a quiet container kept: %v", s.dies)
	}
}

func TestValidateInputBounds(t *testing.T) {
	long := string(make([]byte, maxRuleField+1))
	tests := []struct {
		name string
		in   RuleInput
	}{
		{"restart window over a week", RuleInput{Name: "x", Kind: KindRestartLoop, ContainerFilter: "*", Threshold: 3, DurationSeconds: 8 * 24 * 3600}},
		{"metric lookback over a day", RuleInput{Name: "x", Kind: KindMetric, ContainerFilter: "*", Metric: "cpu_percent", Operator: "gt", Threshold: 1, DurationSeconds: 2 * 24 * 3600}},
		{"oversized scope", RuleInput{Name: "x", Kind: KindOOM, ContainerFilter: "*", ScopeTag: long}},
		{"unknown kind", RuleInput{Name: "x", Kind: "nope", ContainerFilter: "*"}},
	}
	for _, tt := range tests {
		if err := validateInput(tt.in); err == nil {
			t.Errorf("%s: accepted", tt.name)
		}
	}
}
