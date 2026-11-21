package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// A stack's environment override is a deploy setting of that stack: an
// assignment on another stack, or one without stacks.deploy, must not
// read the list or switch the active override.
func TestStackEnvironmentsFollowStackScope(t *testing.T) {
	authSvc, roles, h := newMatrixEnv(t)
	ctx := t.Context()
	for _, name := range []string{"alpha", "beta"} {
		if _, err := h.Stacks.Create(name, "services:\n  app:\n    image: nginx\n", ""); err != nil {
			t.Fatal(err)
		}
		dir, err := h.Stacks.Dir(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "compose.prod.yaml"), []byte("services:\n  app:\n    image: nginx:1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := h.Deployments.Set(ctx, name, "local", "running"); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouter(h, authSvc, nil, true, nil)

	onAlpha := rbac.Scope{Type: rbac.ScopeStack, Host: "local", Value: "alpha"}
	deployerOnAlpha := signIn(t, authSvc, roles, "deployer-alpha", rbac.RoleDeployer, onAlpha)
	// Views every stack but may deploy only alpha.
	uid := createUser(t, authSvc, roles, "viewer-deployer-alpha", rbac.RoleViewer, rbac.Global)
	if _, err := roles.AddAssignment(ctx, rbac.Assignment{UserID: uid, Role: rbac.RoleDeployer, Scope: onAlpha}); err != nil {
		t.Fatal(err)
	}
	viewerDeployer := login(t, authSvc, "viewer-deployer-alpha")
	deployerGlobal := signIn(t, authSvc, roles, "deployer-global", rbac.RoleDeployer, rbac.Global)
	viewerGlobal := signIn(t, authSvc, roles, "viewer-global", rbac.RoleViewer, rbac.Global)

	tests := []struct {
		caller, token, stack string
		want                 int
	}{
		{"deployer on alpha", deployerOnAlpha, "alpha", http.StatusOK},
		{"deployer on alpha", deployerOnAlpha, "beta", http.StatusForbidden},
		{"viewer + deployer on alpha", viewerDeployer, "alpha", http.StatusOK},
		{"viewer + deployer on alpha", viewerDeployer, "beta", http.StatusForbidden},
		{"deployer on everything", deployerGlobal, "beta", http.StatusOK},
		{"viewer on everything", viewerGlobal, "beta", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.caller+" "+tt.stack, func(t *testing.T) {
			call := func(method, path, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "/api/v1/stacks/"+tt.stack+path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+tt.token)
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				return rr
			}
			dir, err := h.Stacks.Dir(tt.stack)
			if err != nil {
				t.Fatal(err)
			}
			metaPath := filepath.Join(dir, stacks.MetaFileName)
			t.Cleanup(func() {
				if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
					t.Error(err)
				}
			})
			if rr := call(http.MethodPut, "/environments/active", `{"active":"prod"}`); rr.Code != tt.want {
				t.Errorf("PUT active: %d %s, want %d", rr.Code, rr.Body, tt.want)
			}
			meta, err := os.ReadFile(metaPath)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if set := strings.Contains(string(meta), `"prod"`); set != (tt.want == http.StatusOK) {
				t.Errorf("active override written=%v after %d", set, tt.want)
			}
			if rr := call(http.MethodGet, "/environments", ""); rr.Code != tt.want {
				t.Errorf("GET environments: %d %s, want %d", rr.Code, rr.Body, tt.want)
			}
		})
	}
}

// Every route on one stack refuses what an assignment on another stack
// grants: a caller with only that assignment never reaches the stack, and
// one who also views everything gets no more than viewing.
func TestStackRoutesRefuseOtherStackScope(t *testing.T) {
	authSvc, roles, h := newMatrixEnv(t)
	ctx := t.Context()
	for _, name := range []string{"alpha", "web"} {
		if _, err := h.Stacks.Create(name, "services:\n  app:\n    image: nginx\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouter(h, authSvc, nil, true, nil)
	onAlpha := rbac.Scope{Type: rbac.ScopeStack, Host: "local", Value: "alpha"}
	viewer := map[rbac.Perm]bool{}
	for _, b := range rbac.BuiltinRoles {
		if b.Name == rbac.RoleViewer {
			viewer = rbac.PermsFor(b.Levels, b.Extras)
		}
	}
	uid := createUser(t, authSvc, roles, "viewer-host-admin-alpha", rbac.RoleViewer, rbac.Global)
	if _, err := roles.AddAssignment(ctx, rbac.Assignment{UserID: uid, Role: rbac.RoleHostAdmin, Scope: onAlpha}); err != nil {
		t.Fatal(err)
	}
	callers := []struct {
		name, token string
		allowed     map[rbac.Perm]bool
	}{
		{"host-admin of alpha", signIn(t, authSvc, roles, "host-admin-alpha", rbac.RoleHostAdmin, onAlpha), nil},
		{"viewer + host-admin of alpha", login(t, authSvc, "viewer-host-admin-alpha"), viewer},
	}
	checked := 0
	for _, g := range routeGates {
		if !strings.HasPrefix(g.path, "/api/v1/stacks/{name}") || g.gate == gatePublic {
			continue
		}
		checked++
		for _, c := range callers {
			// A wrongly allowed route may delete or stop the stack.
			if err := h.Deployments.Set(ctx, "web", "local", "running"); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(g.method, fillPath(g.path), strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+c.token)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if denied := rr.Code == http.StatusForbidden; denied == c.allowed[g.gate] {
				t.Errorf("%s %s on stack web as %s: %d %s, want allowed=%v", g.method, g.path, c.name, rr.Code,
					strings.TrimSpace(rr.Body.String()), c.allowed[g.gate])
			}
		}
	}
	if checked == 0 {
		t.Fatal("no /api/v1/stacks/{name} routes in routeGates")
	}
}
