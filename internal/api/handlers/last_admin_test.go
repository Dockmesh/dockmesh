package handlers

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// An install whose admins all hold narrower scopes must not lose its last
// one: the setup wizard stays closed then, so nobody could manage the
// install any more.
func TestLastScopedAdminIsProtected(t *testing.T) {
	onHost := rbac.Scope{Type: rbac.ScopeHost, Host: "agent-1"}
	type env struct {
		e       *authzEnv
		adminID string
		mgrTok  string
	}
	assignmentOf := func(v env) string {
		return "/assignments/" + strconv.FormatInt(v.e.h.Roles.UserAssignments(v.adminID)[0].ID, 10)
	}
	tests := []struct {
		name        string
		secondAdmin bool
		idp         bool
		run         func(v env) int
		want        int
	}{
		{name: "delete the user", run: func(v env) int {
			return v.e.do(http.MethodDelete, "/users/"+v.adminID, v.mgrTok, nil).Code
		}, want: http.StatusConflict},
		{name: "remove the assignment", run: func(v env) int {
			return v.e.do(http.MethodDelete, assignmentOf(v), v.mgrTok, nil).Code
		}, want: http.StatusConflict},
		{name: "narrow the role", run: func(v env) int {
			if err := v.e.h.checkRoleKeepsAdmin("superops", false); err != nil {
				return http.StatusConflict
			}
			return http.StatusOK
		}, want: http.StatusConflict},
		{name: "identity provider drops it", idp: true, run: func(v env) int {
			if err := v.e.h.SyncIdPAssignments(context.Background(), v.adminID, nil); err != nil {
				return http.StatusInternalServerError
			}
			for _, a := range v.e.h.Roles.UserAssignments(v.adminID) {
				if v.e.h.Roles.IsAdminRole(a.Role) {
					return http.StatusConflict
				}
			}
			return http.StatusOK
		}, want: http.StatusConflict},
		{name: "another scoped admin left", secondAdmin: true, run: func(v env) int {
			return v.e.do(http.MethodDelete, "/users/"+v.adminID, v.mgrTok, nil).Code
		}, want: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newScopeEnv(t)
			admin, _ := rbac.BuiltinRole(rbac.RoleAdmin)
			almost := rbac.Levels{}
			for area, level := range admin.Levels {
				almost[area] = level
			}
			almost["settings"] = "view"
			for name, levels := range map[string]rbac.Levels{"superops": admin.Levels, "usermgr": almost} {
				if err := e.h.Roles.CreateRole(context.Background(), rbac.RoleInput{Name: name, Display: name, Levels: levels, Extras: admin.Extras}); err != nil {
					t.Fatal(err)
				}
			}
			e.roleScopes["superops"] = []rbac.Scope{onHost}
			if e.h.Roles.IsAdminRole("usermgr") || !e.h.Roles.IsAdminRole("superops") {
				t.Fatal("test roles: usermgr must not be an admin role, superops must be one")
			}
			v := env{e: e}
			if tt.idp {
				u := e.user("a", "")
				if _, err := e.h.Roles.AddAssignment(context.Background(), rbac.Assignment{UserID: u.ID, Role: "superops", Scope: onHost, Source: rbac.SourceIdP}); err != nil {
					t.Fatal(err)
				}
				v.adminID = u.ID
			} else {
				v.adminID = e.user("a", "superops").ID
			}
			if tt.secondAdmin {
				e.user("b", "superops")
			}
			e.user("mgr", "usermgr")
			v.mgrTok = e.login("mgr").AccessToken
			if got := tt.run(v); got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}
