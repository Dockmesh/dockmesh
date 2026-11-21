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
	login, err := authSvc.Login(ctx, "viewer-deployer-alpha", "correct horse battery", "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	viewerDeployer := login.AccessToken
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
			if rr := call(http.MethodPut, "/environments/active", `{"active":"prod"}`); rr.Code != tt.want {
				t.Errorf("PUT active: %d %s, want %d", rr.Code, rr.Body, tt.want)
			}
			dir, err := h.Stacks.Dir(tt.stack)
			if err != nil {
				t.Fatal(err)
			}
			meta, _ := os.ReadFile(filepath.Join(dir, stacks.MetaFileName))
			if set := strings.Contains(string(meta), `"prod"`); set != (tt.want == http.StatusOK) {
				t.Errorf("active override written=%v after %d", set, tt.want)
			}
			if err := os.Remove(filepath.Join(dir, stacks.MetaFileName)); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if rr := call(http.MethodGet, "/environments", ""); rr.Code != tt.want {
				t.Errorf("GET environments: %d %s, want %d", rr.Code, rr.Body, tt.want)
			}
		})
	}
}

// Every route on one stack refuses a caller whose assignments name only
// another stack, whatever that role allows there.
func TestStackRoutesRefuseOtherStackScope(t *testing.T) {
	authSvc, roles, h := newMatrixEnv(t)
	ctx := t.Context()
	for _, name := range []string{"alpha", "web"} {
		if _, err := h.Stacks.Create(name, "services:\n  app:\n    image: nginx\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouter(h, authSvc, nil, true, nil)
	token := signIn(t, authSvc, roles, "host-admin-alpha", rbac.RoleHostAdmin, rbac.Scope{Type: rbac.ScopeStack, Host: "local", Value: "alpha"})
	checked := 0
	for _, g := range routeGates {
		if !strings.HasPrefix(g.path, "/api/v1/stacks/{name}") || g.gate == gatePublic || g.gate == gateTicket || g.gate == gateSignedIn {
			continue
		}
		if err := h.Deployments.Set(ctx, "web", "local", "running"); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(g.method, fillPath(g.path), strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		checked++
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s on stack web as host-admin of alpha: %d %s, want 403", g.method, g.path, rr.Code, strings.TrimSpace(rr.Body.String()))
		}
	}
	if checked < 30 {
		t.Errorf("checked %d stack routes; routeGates lists more", checked)
	}
}
