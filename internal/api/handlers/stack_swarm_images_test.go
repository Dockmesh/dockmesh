package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

func TestSwarmImageUpdates(t *testing.T) {
	e, f := newServiceEnv(t)
	old, cur := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	f.images = map[string]string{"web_web": "registry.example/web:1@" + old, "web_db": "postgres:17@" + cur}
	f.digests = map[string]string{"registry.example/web:1": cur, "postgres:17": cur}
	rr := e.do(http.MethodGet, "/api/v1/stacks/web/swarm-image-updates", e.login("root").AccessToken, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d: %s", rr.Code, rr.Body)
	}
	var out swarmImageCheckResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]swarmImageStatus{}
	for _, s := range out.Services {
		got[s.Service] = s
	}
	if w := got["web"]; !w.Update || w.Image != "registry.example/web:1" || w.RunningDigest != old || w.RegistryDigest != cur {
		t.Fatalf("web %+v", w)
	}
	if d := got["db"]; d.Update || d.Error != "" {
		t.Fatalf("db %+v", d)
	}
	if len(f.checks) != 1 || len(f.checks[0].Images) != 2 {
		t.Fatalf("checks %+v", f.checks)
	}
	for _, i := range f.checks[0].Images {
		if (i.Image == "registry.example/web:1") != (i.RegistryAuth == "blob") || strings.Contains(i.Image, "@") {
			t.Fatalf("image ref %+v", i)
		}
	}
	if len(f.actions) != 0 || len(f.deploys) != 1 {
		t.Fatal("the check wrote something")
	}
	delete(f.digests, "postgres:17")
	rr = e.do(http.MethodGet, "/api/v1/stacks/web/swarm-image-updates", e.login("root").AccessToken, nil)
	if !strings.Contains(rr.Body.String(), "manifest unknown") {
		t.Fatalf("registry error not shown: %s", rr.Body)
	}

	e.role("view-c1", []rbac.Perm{rbac.PermStacksView}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.user("u-view", "view-c1")
	if rr := e.do(http.MethodGet, "/api/v1/stacks/web/swarm-image-updates", e.login("u-view").AccessToken, nil); rr.Code != http.StatusForbidden {
		t.Fatalf("viewer: %d", rr.Code)
	}
}
