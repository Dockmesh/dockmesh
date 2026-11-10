package handlers

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func TestScaleAndRollingUpdateRefuseOutsideChanges(t *testing.T) {
	routes := []struct {
		name, path string
		body       map[string]any
	}{
		{"scale", "/stacks/app/services/web/scale", map[string]any{"replicas": 2}},
		{"rolling update", "/stacks/app/services/web/rolling-update", map[string]any{"parallelism": 1}},
	}
	changes := []struct {
		file, content string
	}{
		{"compose.yaml", "services: {web: {image: x, privileged: true}}\n"},
		{stacks.MetaFileName, `{"scaling":{"enabled":true,"rules":[{"service":"web","max_replicas":2}]}}`},
		{"compose.prod.yaml", "services: {web: {privileged: true}}\n"},
	}
	for _, rt := range routes {
		for _, ch := range changes {
			t.Run(rt.name+"/"+ch.file, func(t *testing.T) {
				e := newScopeEnv(t)
				mgr, err := stacks.NewManager(t.TempDir(), nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = mgr.Close() })
				mgr.SetSumStore(stacks.NewSumStore(e.db))
				e.h.Stacks = mgr
				e.h.Hosts = host.NewRegistry(nil, nil)
				e.h.Audit = audit.NewService(e.db, "")
				e.router.Group(func(r chi.Router) {
					r.Use(middleware.NewAuth(e.h.Auth))
					r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/services/{service}/scale", e.h.ScaleService)
					r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/stacks/{name}/services/{service}/rolling-update", e.h.RollingUpdateService)
				})
				e.user("root", "admin")
				tok := e.login("root").AccessToken
				if _, err := mgr.Create("app", "services: {web: {image: x}}\n", ""); err != nil {
					t.Fatal(err)
				}
				dir, err := mgr.Dir("app")
				if err != nil {
					t.Fatal(err)
				}
				writeOutside(t, dir, ch.file, ch.content)

				rr := e.do(http.MethodPost, rt.path, tok, rt.body)
				if rr.Code != http.StatusConflict {
					t.Fatalf("%d, want 409: %s", rr.Code, rr.Body)
				}
				var body struct {
					Code  string   `json:"code"`
					Files []string `json:"files"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Code != "modified_outside" || !slices.Equal(body.Files, []string{ch.file}) {
					t.Fatalf("body = %+v", body)
				}

				rr = e.do(http.MethodPost, rt.path+"?confirm_external=1", tok, rt.body)
				if rr.Code == http.StatusConflict {
					t.Fatalf("confirmed: still 409: %s", rr.Body)
				}
				if changed, err := mgr.ModifiedOutside("app"); err != nil || len(changed) != 0 {
					t.Errorf("after confirm: %v, %v", changed, err)
				}
			})
		}
	}
}
