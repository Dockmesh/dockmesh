package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// A caller limited to some hosts sees only those agents, cannot act on the
// others, and reads only its own audit entries, since entries carry no host.
func TestAgentsAndAuditFollowScope(t *testing.T) {
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
	e.h.Audit = audit.NewService(e.db, "")
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Get("/agents", e.h.ListAgents)
		r.Get("/agents/{id}", e.h.GetAgent)
		r.Delete("/agents/{id}", e.h.DeleteAgent)
		r.Post("/agents/{id}/rotate-token", e.h.RotateAgentEnrollToken)
		r.With(middleware.RequirePerm(rbac.PermAuditView)).Get("/audit", e.h.ListAudit)
	})

	e.role("prod-team", []rbac.Perm{rbac.PermHostsView, rbac.PermAuditView}, rbac.Scope{Type: "host_tag", Value: "prod"})
	boss := e.user("boss", "admin")
	tagged := e.user("prodadmin", "admin")
	e.scopeUser(tagged.ID, "prod")
	team := e.user("team", "prod-team")
	e.h.Audit.Write(ctx, boss.ID, "user.create", "someone", nil)
	e.h.Audit.Write(ctx, tagged.ID, "stack.deploy", "prod-web", nil)
	e.h.Audit.Write(ctx, team.ID, "stack.deploy", "prod-api", nil)

	list := func(user, path string, field func(map[string]any) string) string {
		rr := e.do(http.MethodGet, path, e.login(user).AccessToken, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", user, path, rr.Code, rr.Body)
		}
		var got []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range got {
			out = append(out, field(x))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	id := func(x map[string]any) string { return x["id"].(string) }
	target := func(x map[string]any) string { s, _ := x["target"].(string); return s }

	tests := []struct {
		user, agents, audit string
	}{
		{"boss", "agent-dev,agent-prod", "prod-api,prod-web,someone"},
		{"prodadmin", "agent-prod", ""},
		{"team", "agent-prod", ""},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			if got := list(tt.user, "/agents", id); got != tt.agents {
				t.Errorf("agents %s, want %s", got, tt.agents)
			}
			// The audit log is instance-wide: only an assignment on
			// everything grants it.
			if tt.audit == "" {
				if rr := e.do(http.MethodGet, "/audit", e.login(tt.user).AccessToken, nil); rr.Code != http.StatusForbidden {
					t.Errorf("audit for a scoped caller: %d, want 403", rr.Code)
				}
			} else if got := list(tt.user, "/audit", target); got != tt.audit {
				t.Errorf("audit %s, want %s", got, tt.audit)
			}
		})
	}

	tok := e.login("prodadmin").AccessToken
	if rr := e.do(http.MethodGet, "/agents/agent-dev", tok, nil); rr.Code != http.StatusNotFound {
		t.Errorf("get out-of-scope agent: %d, want 404", rr.Code)
	}
	if rr := e.do(http.MethodPost, "/agents/agent-dev/rotate-token", tok, nil); rr.Code != http.StatusForbidden {
		t.Errorf("rotate out-of-scope agent: %d, want 403", rr.Code)
	}
	if rr := e.do(http.MethodDelete, "/agents/agent-dev", tok, nil); rr.Code != http.StatusForbidden {
		t.Errorf("revoke out-of-scope agent: %d, want 403", rr.Code)
	}
}
