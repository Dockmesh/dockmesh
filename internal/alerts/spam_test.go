package alerts

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// capture records delivered groups instead of sending them.
type capture struct {
	mu     sync.Mutex
	groups []*outGroup
}

func captureSends(s *Service) *capture {
	c := &capture{}
	s.send = func(g *outGroup) {
		c.mu.Lock()
		c.groups = append(c.groups, g)
		c.mu.Unlock()
	}
	return c
}

func (c *capture) all() []*outGroup {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*outGroup(nil), c.groups...)
}

type repeatRow struct {
	notified bool
	repeats  int
}

func repeatsOf(t *testing.T, database *sql.DB, ruleID int64) []repeatRow {
	t.Helper()
	rows, err := database.Query(`SELECT notified, repeats FROM alert_history WHERE rule_id = ? ORDER BY id`, ruleID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []repeatRow
	for rows.Next() {
		var r repeatRow
		var n int
		if err := rows.Scan(&n, &r.repeats); err != nil {
			t.Fatal(err)
		}
		r.notified = n == 1
		out = append(out, r)
	}
	return out
}

func addAgent(t *testing.T, database *sql.DB, id, name string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO agents (id, name, status) VALUES (?, ?, 'online')`, id, name); err != nil {
		t.Fatal(err)
	}
}

func TestOneShotRepeatsInsideCooldownAreCounted(t *testing.T) {
	tests := []struct {
		name  string
		rule  RuleInput
		times []time.Duration
		want  []repeatRow
	}{
		{
			name:  "repeats inside the cooldown fold into the first entry",
			rule:  RuleInput{Kind: KindOOM, CooldownSeconds: 600},
			times: []time.Duration{0, time.Minute, 2 * time.Minute},
			want:  []repeatRow{{true, 2}},
		},
		{
			name:  "a repeat after the cooldown is a new announced entry",
			rule:  RuleInput{Kind: KindOOM, CooldownSeconds: 600},
			times: []time.Duration{0, time.Minute, 11 * time.Minute},
			want:  []repeatRow{{true, 1}, {true, 0}},
		},
		{
			name:  "muted repeats are counted too",
			rule:  RuleInput{Kind: KindOOM, CooldownSeconds: 600, MutedUntil: evalNow.Add(time.Hour).UTC().Format(time.RFC3339)},
			times: []time.Duration{0, time.Minute, 2 * time.Minute},
			want:  []repeatRow{{false, 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, database := newEvalService(t)
			sent := captureSends(s)
			s.groupWindow = 0
			r := eventRule(t, s, tt.rule)
			for _, at := range tt.times {
				s.handleEvent(context.Background(), ev("oom", at))
			}
			got := repeatsOf(t, database, r.ID)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("history = %+v, want %+v", got, tt.want)
			}
			announced := 0
			for _, w := range tt.want {
				if w.notified {
					announced++
				}
			}
			if n := len(sent.all()); n != announced {
				t.Fatalf("sent %d notifications, want %d", n, announced)
			}
			stats, err := s.RuleStatsByID(context.Background(), r.ID, 3650)
			if err != nil {
				t.Fatal(err)
			}
			if stats.FiresCount != len(tt.times) {
				t.Fatalf("fires_count = %d, want %d", stats.FiresCount, len(tt.times))
			}
		})
	}
}

func TestTargetsCappedPerRuleAndHost(t *testing.T) {
	s, database := newEvalService(t)
	s.groupWindow = 0
	captureSends(s)
	r := eventRule(t, s, RuleInput{Kind: KindContainerStopped})
	ctx := context.Background()
	stop := func(host string, i int, at time.Duration) {
		// Spread over seconds so the per-host event rate limit stays out of the way.
		s.handleEvent(ctx, ContainerEvent{HostID: host, HostName: host, Container: fmt.Sprintf("c%d", i),
			Action: "die", ExitCode: "1", Time: evalNow.Add(at)})
	}
	for i := 0; i < maxTargetsPerHost+50; i++ {
		stop("a1", i, time.Duration(i/40)*time.Second)
	}
	stop("a2", 0, time.Hour)

	var perHost = map[string]int{}
	rows, err := database.Query(`SELECT host_id, COUNT(*) FROM alert_history WHERE rule_id = ? GROUP BY host_id`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		var n int
		if err := rows.Scan(&h, &n); err != nil {
			t.Fatal(err)
		}
		perHost[h] = n
	}
	if perHost["a1"] != maxTargetsPerHost || perHost["a2"] != 1 {
		t.Fatalf("history per host = %v, want %d on a1 and 1 on a2", perHost, maxTargetsPerHost)
	}
	if n := s.firing.size(); n != maxTargetsPerHost+1 {
		t.Fatalf("tracked %d firing targets, want %d", n, maxTargetsPerHost+1)
	}
}

func TestBurstOnOneHostIsOneNotification(t *testing.T) {
	s, database := newEvalService(t)
	addAgent(t, database, "a1", "dm-agent1")
	sent := captureSends(s)
	s.groupWindow = time.Hour
	r := eventRule(t, s, RuleInput{Kind: KindContainerStopped})
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		s.handleEvent(ctx, ContainerEvent{HostID: "a1", HostName: "dm-agent1", Container: fmt.Sprintf("app-%d", i),
			Action: "die", ExitCode: "1", Time: evalNow})
	}
	s.handleEvent(ctx, ContainerEvent{HostID: "local", HostName: "Local", Container: "web", Action: "die", ExitCode: "1", Time: evalNow})
	if n := len(sent.all()); n != 0 {
		t.Fatalf("sent %d before the group window closed", n)
	}
	s.flushAll()

	groups := sent.all()
	if len(groups) != 2 {
		t.Fatalf("sent %d notifications, want one per host", len(groups))
	}
	byHost := map[string]*outGroup{}
	for _, g := range groups {
		byHost[g.items[0].target.hostID] = g
	}
	burst := byHost["a1"]
	if burst == nil || burst.n != 12 || len(burst.items) != groupListMax {
		t.Fatalf("agent group = %+v", burst)
	}
	title, body := s.groupText(ctx, burst)
	if want := "🔥 " + r.Name + ": 12 containers on dm-agent1"; title != want {
		t.Errorf("title = %q, want %q", title, want)
	}
	if !strings.HasSuffix(body, "… and 2 more") {
		t.Errorf("body does not end with the remainder: %q", body)
	}
	single, _ := s.groupText(ctx, byHost["local"])
	if single != "🔥 "+r.Name {
		t.Errorf("single alert title = %q", single)
	}

	// All twelve come back: one resolved message, not twelve.
	for i := 0; i < 12; i++ {
		s.handleEvent(ctx, ContainerEvent{HostID: "a1", HostName: "dm-agent1", Container: fmt.Sprintf("app-%d", i),
			Action: "start", Time: evalNow.Add(time.Minute)})
	}
	s.flushAll()
	groups = sent.all()[2:]
	if len(groups) != 1 || !groups[0].resolved || groups[0].n != 12 {
		t.Fatalf("resolve notifications = %+v", groups)
	}
}

func TestHistoryRetention(t *testing.T) {
	s, database := newEvalService(t)
	stopped := eventRule(t, s, RuleInput{Kind: KindContainerStopped})
	oom := eventRule(t, s, RuleInput{Kind: KindOOM})
	old := dbTime(time.Now().Add(-40 * 24 * time.Hour))
	recent := dbTime(time.Now().Add(-time.Hour))
	insert := func(rule int64, name, status, at string) {
		t.Helper()
		if _, err := database.Exec(`INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, notified, host_id, occurred_at)
			VALUES (?, 'r', ?, ?, '', 1, 'local', ?)`, rule, name, status, at); err != nil {
			t.Fatal(err)
		}
	}
	insert(stopped.ID, "gone", "fired", old)
	insert(stopped.ID, "gone", "resolved", old)
	insert(stopped.ID, "still-down", "fired", old)
	insert(oom.ID, "oom-old", "fired", old)
	insert(oom.ID, "oom-new", "fired", recent)
	insert(999, "deleted-rule", "fired", old)

	s.pruneHistory(context.Background(), time.Now())

	rows, err := database.Query(`SELECT container_name FROM alert_history ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var left []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		left = append(left, n)
	}
	if want := []string{"still-down", "oom-new"}; !equalStrings(left, want) {
		t.Fatalf("kept %v, want %v", left, want)
	}

	restarted := NewService(database, nil)
	if err := restarted.loadFiringState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !restarted.isFiring(tkey{stopped.ID, "local", "still-down"}) {
		t.Fatal("active alert lost after prune and restart")
	}
}

func TestPruneStateDropsQuietCooldowns(t *testing.T) {
	s, _ := newEvalService(t)
	s.groupWindow = 0
	captureSends(s)
	eventRule(t, s, RuleInput{Kind: KindOOM, CooldownSeconds: 300})
	s.handleEvent(context.Background(), ev("oom", 0))
	if s.lastNotified.size() != 1 || s.lastRows.size() != 1 {
		t.Fatalf("state after fire: notified %d rows %d", s.lastNotified.size(), s.lastRows.size())
	}
	s.pruneState(evalNow.Add(4 * time.Minute))
	if s.lastNotified.size() != 1 {
		t.Fatal("cooldown dropped while still running")
	}
	s.pruneState(evalNow.Add(6 * time.Minute))
	if s.lastNotified.size() != 0 || s.lastRows.size() != 0 {
		t.Fatalf("quiet target still tracked: notified %d rows %d", s.lastNotified.size(), s.lastRows.size())
	}
}

func TestHistoryLimitAppliesAfterScopeFilter(t *testing.T) {
	s, database := newEvalService(t)
	r := eventRule(t, s, RuleInput{Kind: KindOOM})
	for i := 0; i < 30; i++ {
		host := "other"
		if i%10 == 0 {
			host = "mine"
		}
		if _, err := database.Exec(`INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, host_id)
			VALUES (?, 'r', ?, 'fired', '', ?)`, r.ID, fmt.Sprintf("c%d", i), host); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.History(context.Background(), 2, func(e HistoryEntry) bool { return e.HostID == "mine" })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].HostID != "mine" || got[1].HostID != "mine" {
		t.Fatalf("history = %+v, want the two newest entries of host mine", got)
	}
}
