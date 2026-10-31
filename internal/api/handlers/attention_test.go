package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	dtypes "github.com/docker/docker/api/types"

	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

func attentionRow(id, name, stack, state, status string) containerRow {
	return containerRow{
		Container: dtypes.Container{
			ID:     id,
			Names:  []string{"/" + name},
			State:  state,
			Status: status,
			Labels: map[string]string{"com.docker.compose.project": stack},
		},
		HostID:       "local",
		HostName:     "Local",
		HealthStatus: parseHealthFromStatus(status),
	}
}

func newAttentionEnv(t *testing.T) *authzEnv {
	t.Helper()
	e := newAuthzEnv(t)
	e.h.Backups = backup.NewService(e.db, nil, nil, nil, backup.SystemPaths{})
	e.h.Alerts = alerts.NewService(e.db, nil)
	e.h.attentionRows = func(context.Context) []containerRow {
		return []containerRow{
			attentionRow("c1", "web-1", "web", "running", "Up 2 hours (unhealthy)"),
			attentionRow("c2", "db-1", "db", "restarting", "Restarting (1) 5 seconds ago"),
			attentionRow("c3", "web-migrate", "web", "exited", "Exited (2) 1 hour ago"),
			attentionRow("c4", "web-cron", "web", "exited", "Exited (0) 1 hour ago"),
			attentionRow("c5", "cache", "", "running", "Up 3 days"),
		}
	}
	e.router.With(middleware.NewAuth(e.h.Auth)).Get("/attention", e.h.Attention)

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	jobs := []struct {
		name, sources string
	}{
		{"db-nightly", `[{"type":"stack","name":"db"}]`},
		{"web-nightly", `[{"type":"stack","name":"web"}]`},
		{"server", `[{"type":"system","name":"system"}]`},
		{"web-ok", `[{"type":"stack","name":"web"}]`},
	}
	for i, j := range jobs {
		exec(`INSERT INTO backup_jobs (name, target_type, sources) VALUES (?, 'local', ?)`, j.name, j.sources)
		status := "failed"
		if j.name == "web-ok" {
			exec(`INSERT INTO backup_runs (job_id, job_name, status, error) VALUES (?, ?, 'failed', 'old failure')`, i+1, j.name)
			status = "success"
		}
		exec(`INSERT INTO backup_runs (job_id, job_name, status, error, sources_json) VALUES (?, ?, ?, 'disk full', ?)`, i+1, j.name, status, j.sources)
	}
	exec(`INSERT INTO alert_rules (id, name, metric, operator, threshold, severity) VALUES (900, 'High CPU', 'cpu_percent', 'gt', 90, 'critical')`)
	exec(`INSERT INTO alert_history (rule_id, rule_name, container_name, status, message) VALUES (900, 'High CPU', 'db-1', 'fired', 'cpu high')`)
	exec(`INSERT INTO alert_history (rule_id, rule_name, container_name, status, message) VALUES (900, 'High CPU', 'web-1', 'fired', 'cpu high')`)
	exec(`INSERT INTO alert_history (rule_id, rule_name, container_name, status, message) VALUES (900, 'High CPU', 'web-1', 'resolved', 'ok')`)
	return e
}

func TestAttentionScope(t *testing.T) {
	e := newAttentionEnv(t)
	views := []rbac.Perm{rbac.PermContainersView, rbac.PermBackupsView, rbac.PermAlertsView, rbac.PermHostsView}
	e.role("web-team", views, rbac.Scope{ScopeType: "stack", ScopeValue: "web"})
	e.role("containers-only", []rbac.Perm{rbac.PermContainersView})
	e.user("boss", "admin")
	e.user("web", "web-team")
	e.user("narrow", "containers-only")

	tests := []struct {
		user string
		want []string
	}{
		{"boss", []string{
			"alert_firing:900:db-1",
			"backup_failed:1",
			"backup_failed:2",
			"backup_failed:3",
			"container_restarting:local:c2",
			"container_exited:local:c3",
			"container_unhealthy:local:c1",
		}},
		{"web", []string{
			"backup_failed:2",
			"container_exited:local:c3",
			"container_unhealthy:local:c1",
		}},
		{"narrow", []string{
			"container_restarting:local:c2",
			"container_exited:local:c3",
			"container_unhealthy:local:c1",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			rr := e.do(http.MethodGet, "/attention", e.login(tt.user).AccessToken, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rr.Code, rr.Body)
			}
			var items []AttentionItem
			if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, it := range items {
				got = append(got, it.ID)
			}
			sort.Strings(got)
			want := append([]string(nil), tt.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("items = %v, want %v", got, want)
			}
			for i := 1; i < len(items); i++ {
				if severityRank(items[i-1].Severity) > severityRank(items[i].Severity) {
					t.Errorf("item %d (%s) sorted before a more severe one", i-1, items[i-1].ID)
				}
			}
		})
	}
}

func TestAttentionUnauthenticated(t *testing.T) {
	e := newAttentionEnv(t)
	if rr := e.do(http.MethodGet, "/attention", "", nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rr.Code)
	}
}

func TestSortAttention(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	items := []AttentionItem{
		{ID: "info", Severity: attentionInfo, Since: &newer},
		{ID: "warn-none", Severity: attentionWarning},
		{ID: "warn-old", Severity: attentionWarning, Since: &older},
		{ID: "crit", Severity: attentionCritical},
		{ID: "warn-new", Severity: attentionWarning, Since: &newer},
	}
	sortAttention(items)
	var got []string
	for _, it := range items {
		got = append(got, it.ID)
	}
	want := []string{"crit", "warn-new", "warn-old", "warn-none", "info"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		status string
		want   int
	}{
		{"Exited (0) 3 hours ago", 0},
		{"Exited (137) 2 minutes ago", 137},
		{"Exited (-1) 1 second ago", -1},
		{"Up 5 minutes", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := exitCode(tt.status); got != tt.want {
			t.Errorf("exitCode(%q) = %d, want %d", tt.status, got, tt.want)
		}
	}
}
