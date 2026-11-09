package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type stackDetailBody struct {
	Compose        string `json:"compose"`
	Env            string `json:"env"`
	ComposeVersion string `json:"compose_version"`
	EnvVersion     string `json:"env_version"`
	EnvHidden      bool   `json:"env_hidden"`
}

type stackConflictBody struct {
	Code    string          `json:"code"`
	Files   []string        `json:"files"`
	Current stackDetailBody `json:"current"`
}

func stackSaveEnv(t *testing.T) (*authzEnv, string, string) {
	t.Helper()
	e := newScopeEnv(t)
	root := t.TempDir()
	mgr, err := stacks.NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	e.h.Stacks = mgr
	e.h.Audit = audit.NewService(e.db, "")
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksView)).Get("/save/{name}", e.h.GetStack)
		r.With(middleware.RequirePerm(rbac.PermStacksUpdate)).Put("/save/{name}", e.h.UpdateStack)
	})
	// No role level grants update without deploy; an identity does.
	e.router.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				access := &rbac.Access{Authenticated: true, Grants: []rbac.Grant{{Perms: map[rbac.Perm]bool{rbac.PermStacksView: true, rbac.PermStacksUpdate: true}}}}
				next.ServeHTTP(w, req.WithContext(middleware.WithIdentity(req.Context(), middleware.Identity{UserID: "ed", Access: access})))
			})
		})
		r.Get("/limited/{name}", e.h.GetStack)
		r.Put("/limited/{name}", e.h.UpdateStack)
	})
	e.user("root", "admin")
	if _, err := mgr.Create("app", "a: 1\n", "SECRET=hunter2\n"); err != nil {
		t.Fatal(err)
	}
	return e, filepath.Join(root, "app"), e.login("root").AccessToken
}

func loadStack(t *testing.T, e *authzEnv, path, tok string) stackDetailBody {
	t.Helper()
	rr := e.do(http.MethodGet, path, tok, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rr.Code, rr.Body)
	}
	var d stackDetailBody
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func writeOutside(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSaveRefusedWhenFileChangedOnDisk(t *testing.T) {
	e, dir, root := stackSaveEnv(t)
	d := loadStack(t, e, "/save/app", root)
	if d.ComposeVersion == "" || d.EnvVersion == "" {
		t.Fatalf("versions missing: %+v", d)
	}
	writeOutside(t, dir, "compose.yaml", "a: 9\n")

	rr := e.do(http.MethodPut, "/save/app", root, map[string]any{
		"compose": "a: 2\n", "env": d.Env, "compose_version": d.ComposeVersion, "env_version": d.EnvVersion,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("%d, want 409: %s", rr.Code, rr.Body)
	}
	var body stackConflictBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "stack_modified_on_disk" || len(body.Files) != 1 || body.Files[0] != "compose.yaml" {
		t.Errorf("body = %+v", body)
	}
	if body.Current.Compose != "a: 9\n" || body.Current.Env != d.Env {
		t.Errorf("current = %+v", body.Current)
	}
	if got := loadStack(t, e, "/save/app", root); got.Compose != "a: 9\n" {
		t.Errorf("refused save wrote compose %q", got.Compose)
	}

	rr = e.do(http.MethodPut, "/save/app", root, map[string]any{
		"compose": "a: 2\n", "env": d.Env, "compose_version": d.ComposeVersion, "env_version": d.EnvVersion, "force": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("force: %d %s", rr.Code, rr.Body)
	}
	if got := loadStack(t, e, "/save/app", root); got.Compose != "a: 2\n" {
		t.Errorf("forced save left compose %q", got.Compose)
	}
	var details string
	if err := e.db.QueryRow(`SELECT details FROM audit_log WHERE action = ? AND target = 'app'`, audit.ActionStackForceSave).Scan(&details); err != nil {
		t.Fatalf("audit entry: %v", err)
	}
	if !strings.Contains(details, "compose.yaml") {
		t.Errorf("audit details = %s", details)
	}
}

func TestSaveWithoutVersionStillWorks(t *testing.T) {
	e, dir, root := stackSaveEnv(t)
	writeOutside(t, dir, "compose.yaml", "a: 9\n")
	rr := e.do(http.MethodPut, "/save/app", root, map[string]any{"compose": "a: 2\n", "env": "SECRET=hunter2\n"})
	if rr.Code != http.StatusOK {
		t.Fatalf("%d: %s", rr.Code, rr.Body)
	}
	if got := loadStack(t, e, "/save/app", root); got.Compose != "a: 2\n" {
		t.Errorf("compose = %q", got.Compose)
	}
}

func TestSaveNeverExposesOrDropsHiddenEnv(t *testing.T) {
	e, dir, root := stackSaveEnv(t)
	d := loadStack(t, e, "/limited/app", "")
	if !d.EnvHidden || d.Env != "" || d.EnvVersion != "" {
		t.Fatalf("env leaked on load: %+v", d)
	}

	writeOutside(t, dir, "compose.yaml", "a: 9\n")
	rr := e.do(http.MethodPut, "/limited/app", "", map[string]any{"compose": "a: 2\n", "compose_version": d.ComposeVersion})
	if rr.Code != http.StatusConflict {
		t.Fatalf("%d, want 409: %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), "hunter2") {
		t.Errorf("conflict body leaks .env: %s", rr.Body)
	}
	var body stackConflictBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Current.EnvHidden || body.Current.EnvVersion != "" {
		t.Errorf("current = %+v", body.Current)
	}

	rr = e.do(http.MethodPut, "/limited/app", "", map[string]any{"compose": "a: 2\n", "env": "SECRET=changed\n", "force": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("force: %d %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), "hunter2") || strings.Contains(rr.Body.String(), "changed") {
		t.Errorf("save response leaks .env: %s", rr.Body)
	}
	if got := loadStack(t, e, "/save/app", root); got.Env != "SECRET=hunter2\n" {
		t.Errorf(".env = %q, want it unchanged", got.Env)
	}

	writeOutside(t, dir, ".env", "SECRET=rotated\n")
	rr = e.do(http.MethodPut, "/limited/app", "", map[string]any{"compose": "a: 3\n", "env_version": "wrong"})
	if rr.Code != http.StatusOK {
		t.Errorf("an env version must not act as an oracle for a hidden .env: %d %s", rr.Code, rr.Body)
	}
}
