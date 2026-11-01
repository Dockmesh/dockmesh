package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/system"
)

// Host history follows the caller's hosts, and a host without history
// answers with an explicit empty state instead of 404.
func TestHostMetricsScopeAndEmptyState(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	for _, id := range []string{"agent-prod", "agent-dev"} {
		if _, err := e.db.ExecContext(ctx, `INSERT INTO agents (id, name, status) VALUES (?, ?, 'offline')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "agent-prod", []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	e.h.Agents = agents.NewService(e.db, nil, "http://dockmesh.test", "wss://dockmesh.test/connect")
	e.h.Metrics = metrics.NewCollector(e.db, nil, time.Minute, metrics.DefaultRetention)
	if err := e.h.Metrics.InsertHost(ctx, "local", time.Now().Unix(), system.Metrics{CPUPercent: 12, CPUCores: 2}); err != nil {
		t.Fatal(err)
	}
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermHostsView)).Get("/hosts/{id}/metrics", e.h.GetHostMetrics)
		r.With(middleware.RequirePerm(rbac.PermContainersView)).Get("/hosts/{id}/stats/containers", e.h.BatchContainerStats)
	})
	e.role("prod-team", []rbac.Perm{rbac.PermHostsView, rbac.PermContainersView}, rbac.Scope{Type: "host_tag", Value: "prod"})
	e.user("boss", "admin")
	e.user("team", "prod-team")

	tests := []struct {
		user, path   string
		code         int
		history      bool
		reason       string
		samples      int
		samplesField string
	}{
		{"boss", "/hosts/local/metrics", http.StatusOK, true, "", 1, "samples"},
		{"boss", "/hosts/agent-dev/metrics", http.StatusOK, false, "agent_no_history", 0, "samples"},
		{"boss", "/hosts/nope/metrics", http.StatusNotFound, false, "", 0, ""},
		{"boss", "/hosts/agent-dev/stats/containers", http.StatusOK, false, "agent_no_history", 0, "containers"},
		{"team", "/hosts/agent-prod/metrics", http.StatusOK, false, "agent_no_history", 0, "samples"},
		{"team", "/hosts/local/metrics", http.StatusForbidden, false, "", 0, ""},
		{"team", "/hosts/agent-dev/metrics", http.StatusForbidden, false, "", 0, ""},
		{"team", "/hosts/agent-prod/stats/containers", http.StatusOK, false, "agent_no_history", 0, "containers"},
		{"team", "/hosts/agent-dev/stats/containers", http.StatusForbidden, false, "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.user+" "+tt.path, func(t *testing.T) {
			rr := e.do(http.MethodGet, tt.path, e.login(tt.user).AccessToken, nil)
			if rr.Code != tt.code {
				t.Fatalf("code = %d, want %d: %s", rr.Code, tt.code, rr.Body)
			}
			if tt.code != http.StatusOK {
				return
			}
			var got map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			reason, _ := got["reason"].(string)
			list, ok := got[tt.samplesField].([]any)
			if got["history"] != tt.history || reason != tt.reason || !ok || len(list) != tt.samples {
				t.Errorf("body = %s, want history=%v reason=%q %d %s", rr.Body, tt.history, tt.reason, tt.samples, tt.samplesField)
			}
		})
	}
}
