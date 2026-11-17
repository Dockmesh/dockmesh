package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// POST /agents takes tags and a stacks directory. Creating hosts and
// tagging them are global-only, so a tag-scoped admin can do neither.
func TestCreateAgentWithTagsAndStacksDir(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags
	mgr, err := pki.New(t.TempDir(), []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	e.h.Agents = agents.NewService(e.db, mgr, "http://dockmesh.test", "")
	e.h.Audit = audit.NewService(e.db, "")
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermHostsCreate)).Post("/agents", e.h.CreateAgent)
	})
	e.user("boss", "admin")
	prod := e.user("prodadmin", "admin")
	e.scopeUser(prod.ID, "prod")

	tests := []struct {
		name, user string
		body       map[string]any
		want       int
		wantTags   []string
		wantDir    string
	}{
		{"tags and stacks dir", "boss", map[string]any{"name": "edge-1", "tags": []string{"prod", "edge", "prod"}, "stacks_dir": "/srv/stacks"}, http.StatusCreated, []string{"edge", "prod"}, "/srv/stacks"},
		{"no name", "boss", map[string]any{}, http.StatusCreated, nil, ""},
		{"name taken", "boss", map[string]any{"name": "edge-1"}, http.StatusConflict, nil, ""},
		{"bad tag", "boss", map[string]any{"name": "edge-2", "tags": []string{"Not OK"}}, http.StatusBadRequest, nil, ""},
		{"relative stacks dir", "boss", map[string]any{"name": "edge-3", "stacks_dir": "srv/stacks"}, http.StatusBadRequest, nil, ""},
		{"tag-scoped admin", "prodadmin", map[string]any{"name": "edge-4", "tags": []string{"prod"}}, http.StatusForbidden, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rr := e.do(http.MethodPost, "/agents", e.login(tc.user).AccessToken, tc.body)
			if rr.Code != tc.want {
				t.Fatalf("status %d %s, want %d", rr.Code, rr.Body, tc.want)
			}
			if rr.Code != http.StatusCreated {
				return
			}
			var res agents.CreateResult
			if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if !res.Agent.NeedsSetup {
				t.Error("new host does not wait for setup")
			}
			if got := tags.Tags(res.Agent.ID); !slices.Equal(got, tc.wantTags) {
				t.Errorf("tags %v, want %v", got, tc.wantTags)
			}
			gotDir := ""
			if res.Agent.StacksDir != nil {
				gotDir = res.Agent.StacksDir.Path
			}
			if gotDir != tc.wantDir {
				t.Errorf("stacks dir %q, want %q", gotDir, tc.wantDir)
			}
		})
	}
	var n int
	if err := e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE name IN ('edge-2', 'edge-3', 'edge-4')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused requests left %d hosts (%v)", n, err)
	}
}
