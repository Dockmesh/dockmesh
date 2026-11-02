package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// A token minted by a token never holds more than its parent holds at the
// time of use: widening the child's role later doesn't widen the child,
// and revoking the parent ends the child.
func TestChildTokenStaysWithinParent(t *testing.T) {
	e := newAuthzEnv(t)
	e.user("root", "admin")
	admin := e.login("root").AccessToken
	e.role("narrow", []rbac.Perm{rbac.PermContainersView})
	e.role("child", []rbac.Perm{rbac.PermContainersView})

	parent, parentID := e.apiTokenWith(admin, map[string]any{"name": "parent", "role": "narrow"})
	child, _ := e.apiTokenWith(parent, map[string]any{"name": "child", "role": "child"})
	if rr := e.do(http.MethodGet, "/needs-deploy", child, nil); rr.Code != http.StatusForbidden {
		t.Fatalf("child before the role edit: %d, want 403", rr.Code)
	}

	levels, extras := levelsGranting([]rbac.Perm{rbac.PermContainersView, rbac.PermStacksDeploy})
	if err := e.h.Roles.UpdateRole(context.Background(), "child", rbac.RoleInput{Name: "child", Display: "child", Levels: levels, Extras: extras}); err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodGet, "/needs-deploy", child, nil); rr.Code != http.StatusForbidden {
		t.Errorf("child after widening its role: %d, want 403 (parent has no stacks.deploy)", rr.Code)
	}
	if rr := e.do(http.MethodGet, "/me", child, nil); rr.Code != http.StatusOK {
		t.Fatalf("child with a live parent: %d", rr.Code)
	}

	if err := e.h.APITokens.Revoke(context.Background(), parentID); err != nil {
		t.Fatal(err)
	}
	if rr := e.do(http.MethodGet, "/me", child, nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("child of a revoked parent: %d, want 401", rr.Code)
	}
}

// Audit entries written for an API token name its owner and the token.
func TestAuditNamesTokenOwner(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.Audit = audit.NewService(e.db, "")
	u := e.user("root", "admin")
	tok, tokID := e.apiTokenWith(e.login("root").AccessToken, map[string]any{"name": "ci"})
	e.apiTokenWith(tok, map[string]any{"name": "minted"})

	entries, err := e.h.Audit.ListFiltered(context.Background(), audit.ListFilter{Action: audit.ActionAPITokenCreate})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, en := range entries {
		var d map[string]any
		_ = json.Unmarshal([]byte(en.Details), &d)
		if d["name"] != "minted" {
			continue
		}
		found = true
		if en.UserID != u.ID {
			t.Errorf("user_id = %q, want the token owner %q", en.UserID, u.ID)
		}
		if got, _ := d["api_token_id"].(float64); int64(got) != tokID {
			t.Errorf("api_token_id = %v, want %d", d["api_token_id"], tokID)
		}
	}
	if !found {
		t.Fatal("no audit entry for the token-minted token")
	}
}
