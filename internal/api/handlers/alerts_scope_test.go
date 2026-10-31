package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/go-chi/chi/v5"
)

// A host-scoped user may only manage event rules bound to one of their
// hosts and only sees history from those hosts; otherwise a catch-all
// rule would forward other teams' events to the user's own channel.
func TestAlertRulesRespectHostScope(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tags.Set(ctx, "agent-prod", []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	e.h.Alerts = alerts.NewService(e.db, nil)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Post("/alerts/rules", e.h.CreateAlertRule)
		r.Put("/alerts/rules/{id}", e.h.UpdateAlertRule)
		r.Get("/alerts/history", e.h.ListAlertHistory)
	})

	e.user("global", "admin")
	scoped := e.user("scoped", "admin")
	if _, err := e.h.Auth.UpdateUserScope(ctx, scoped.ID, []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	globalTok := e.login("global").AccessToken
	scopedTok := e.login("scoped").AccessToken

	rule := func(scopeHost string) map[string]any {
		return map[string]any{"name": "oom", "kind": "oom", "container_filter": "*", "scope_host": scopeHost, "enabled": true}
	}
	tests := []struct {
		name  string
		token string
		body  map[string]any
		want  int
	}{
		{"unscoped user, catch-all rule", globalTok, rule(""), http.StatusCreated},
		{"scoped user, catch-all rule", scopedTok, rule(""), http.StatusForbidden},
		{"scoped user, other host", scopedTok, rule("agent-other"), http.StatusForbidden},
		{"scoped user, own host", scopedTok, rule("agent-prod"), http.StatusCreated},
		{"scoped user, global backup rule", scopedTok, map[string]any{"name": "b", "kind": "backup_failed", "container_filter": "*", "scope_host": "agent-prod"}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rr := e.do(http.MethodPost, "/alerts/rules", tt.token, tt.body); rr.Code != tt.want {
				t.Fatalf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}

	if rr := e.do(http.MethodPut, "/alerts/rules/1", scopedTok, rule("agent-prod")); rr.Code != http.StatusForbidden {
		t.Errorf("scoped user rescoping a catch-all rule: %d, want 403", rr.Code)
	}

	if _, err := e.db.Exec(`INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, host_id)
		VALUES (1, 'oom', 'a', 'fired', 'm', 'agent-prod'), (1, 'oom', 'b', 'fired', 'm', 'agent-other'), (1, 'oom', 'c', 'fired', 'm', '')`); err != nil {
		t.Fatal(err)
	}
	for tok, want := range map[string]int{globalTok: 3, scopedTok: 1} {
		rr := e.do(http.MethodGet, "/alerts/history", tok, nil)
		var got []alerts.HistoryEntry
		if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got) != want {
			t.Errorf("history entries = %d, want %d", len(got), want)
		}
	}
}
