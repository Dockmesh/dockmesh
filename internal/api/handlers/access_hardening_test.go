package handlers

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/oidc"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Configuring an identity provider hands out its default role and group
// mappings at sign-in, so it needs the right to grant them.
func TestIdPConfigCannotGrantMoreThanCaller(t *testing.T) {
	e := newScopeEnv(t)
	e.h.OIDC = oidc.NewService(e.db, e.h.Auth, nil, "http://dockmesh.test")
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermSystemUpdate)).Post("/oidc/providers", e.h.CreateOIDCProvider)
	})
	e.role("settings-mgr", []rbac.Perm{rbac.PermSystemUpdate})
	e.user("ops", "settings-mgr")
	tok := e.login("ops").AccessToken
	provider := func(defaultRole string, mappings []map[string]any) map[string]any {
		return map[string]any{"slug": "corp", "display_name": "Corp", "issuer_url": "https://idp.test", "client_id": "c",
			"client_secret": "s", "default_role": defaultRole, "group_mappings": mappings}
	}
	if rr := e.do(http.MethodPost, "/oidc/providers", tok, provider("admin", nil)); rr.Code != http.StatusForbidden {
		t.Errorf("default role admin: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodPost, "/oidc/providers", tok, provider("", []map[string]any{{"group_value": "g", "role_name": "admin"}})); rr.Code != http.StatusForbidden {
		t.Errorf("group mapping to admin: %d %s", rr.Code, rr.Body)
	}
}

// Deleting a user drops its assignments from the cache at once, so a
// deleted admin no longer keeps the last-admin guard open.
func TestDeletedAdminNoLongerCounts(t *testing.T) {
	e := newScopeEnv(t)
	a := e.user("a", "admin")
	b := e.user("b", "admin")
	tok := e.login("a").AccessToken
	if rr := e.do(http.MethodDelete, "/users/"+b.ID, tok, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("delete b: %d %s", rr.Code, rr.Body)
	}
	own := e.h.Roles.UserAssignments(a.ID)[0].ID
	if rr := e.do(http.MethodDelete, "/assignments/"+strconv.FormatInt(own, 10), tok, nil); rr.Code != http.StatusConflict {
		t.Errorf("remove the last admin assignment after deleting the other admin: %d %s", rr.Code, rr.Body)
	}
}

// A drain moves stacks onto any host, so a host-scoped manager can't run one.
func TestDrainNeedsHostsOnEverything(t *testing.T) {
	e := newScopeEnv(t)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermHostsUpdate)).Post("/hosts/{id}/drain/plan", e.h.PlanDrain)
	})
	e.roleScopes["host-admin"] = []rbac.Scope{{Type: rbac.ScopeHost, Host: "local"}}
	e.user("ha", "host-admin")
	if rr := e.do(http.MethodPost, "/hosts/local/drain/plan", e.login("ha").AccessToken, nil); rr.Code != http.StatusForbidden {
		t.Errorf("host-scoped drain plan: %d %s", rr.Code, rr.Body)
	}
}
