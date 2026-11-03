package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// An admin lists another user's sessions and ends one or all of them; a
// caller who can't manage the user gets 403.
func TestAdminManagesUserSessions(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.DB = e.db
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(middleware.RequirePerm(rbac.PermUsersUpdate))
		r.Get("/users/{id}/sessions", e.h.ListUserSessions)
		r.Delete("/users/{id}/sessions/{family_id}", e.h.RevokeUserSession)
		r.Post("/users/{id}/sessions/revoke-all", e.h.RevokeUserSessions)
	})
	e.user("root", "admin")
	pat := e.user("pat", "viewer")
	e.role("usermgr", []rbac.Perm{rbac.PermUsersView, rbac.PermUsersUpdate})
	e.user("mgr", "usermgr")
	admin := e.login("root")
	first, second := e.login("pat"), e.login("pat")

	rr := e.do(http.MethodGet, "/users/"+pat.ID+"/sessions", admin.AccessToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	var list []Session
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("sessions = %d, want 2", len(list))
	}
	if rr := e.do(http.MethodDelete, "/users/"+pat.ID+"/sessions/"+list[0].FamilyID, admin.AccessToken, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke one: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do(http.MethodDelete, "/users/"+pat.ID+"/sessions/"+list[0].FamilyID, admin.AccessToken, nil); rr.Code != http.StatusNotFound {
		t.Errorf("revoke it again: %d, want 404", rr.Code)
	}
	alive := 0
	for _, s := range []string{first.AccessToken, second.AccessToken} {
		if rr := e.do(http.MethodGet, "/me", s, nil); rr.Code == http.StatusOK {
			alive++
		}
	}
	if alive != 1 {
		t.Errorf("live sessions after revoking one = %d, want 1", alive)
	}

	if rr := e.do(http.MethodPost, "/users/"+pat.ID+"/sessions/revoke-all", admin.AccessToken, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke all: %d %s", rr.Code, rr.Body)
	}
	for _, s := range []string{first.AccessToken, second.AccessToken} {
		if rr := e.do(http.MethodGet, "/me", s, nil); rr.Code != http.StatusUnauthorized {
			t.Errorf("session alive after revoke-all: %d", rr.Code)
		}
	}

	mgr := e.login("mgr").AccessToken
	rootID := e.h.Roles.RoleAssignments("admin")[0].UserID
	if rr := e.do(http.MethodGet, "/users/"+rootID+"/sessions", mgr, nil); rr.Code != http.StatusForbidden {
		t.Errorf("user manager listing an admin's sessions: %d, want 403", rr.Code)
	}
}

// Sign out everywhere from My account ends every session of the caller,
// including the one making the request, and no one else's.
func TestRevokeAllMySessions(t *testing.T) {
	e := newAuthzEnv(t)
	e.h.DB = e.db
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Post("/sessions/revoke-all", e.h.RevokeAllMySessions)
	})
	e.user("pat", "viewer")
	e.user("sam", "viewer")
	first, second := e.login("pat"), e.login("pat")
	other := e.login("sam")

	if rr := e.do(http.MethodPost, "/sessions/revoke-all", first.AccessToken, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke all: %d %s", rr.Code, rr.Body)
	}
	for name, tok := range map[string]string{"calling session": first.AccessToken, "other session": second.AccessToken} {
		if rr := e.do(http.MethodGet, "/me", tok, nil); rr.Code != http.StatusUnauthorized {
			t.Errorf("%s alive after sign out everywhere: %d", name, rr.Code)
		}
	}
	if rr := e.do(http.MethodGet, "/me", other.AccessToken, nil); rr.Code != http.StatusOK {
		t.Errorf("another user's session ended: %d", rr.Code)
	}
}
