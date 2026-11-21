package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

func (e *swarmDeployEnv) scale(user, stack, service string, body scaleRequest) (int, string) {
	rr := e.do(http.MethodPost, "/api/v1/stacks/"+stack+"/services/"+service+"/scale", e.login(user).AccessToken, body)
	return rr.Code, rr.Body.String()
}

func TestSwarmScale(t *testing.T) {
	e, f := newServiceEnv(t)
	code, body := e.scale("root", "web", "web", scaleRequest{Replicas: 4})
	if code != http.StatusOK {
		t.Fatalf("scale %d: %s", code, body)
	}
	var out swarmScaleResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Previous != 1 || out.Current != 4 || out.Created != 3 || out.ClusterID != "c1" {
		t.Fatalf("answer %s", body)
	}
	if len(f.actions) != 1 || f.actions[0].Action != agents.SwarmServiceScale || *f.actions[0].Replicas != 4 || f.actions[0].Service != "web_web" {
		t.Fatalf("actions %+v", f.actions)
	}
	for _, tc := range []struct {
		name     string
		service  string
		replicas int
		want     int
		contains string
	}{
		{"over 100", "web", 101, http.StatusBadRequest, "between 0 and 100"},
		{"negative", "web", -1, http.StatusBadRequest, "between 0 and 100"},
		{"unknown service", "nope", 2, http.StatusNotFound, ""},
	} {
		if code, body := e.scale("root", "web", tc.service, scaleRequest{Replicas: tc.replicas}); code != tc.want || !strings.Contains(body, tc.contains) {
			t.Errorf("%s: %d: %s", tc.name, code, body)
		}
	}
	f.setState("paused")
	if code, body := e.scale("root", "web", "web", scaleRequest{Replicas: 2}); code != http.StatusConflict || !strings.Contains(body, "swarm_update_unfinished") {
		t.Errorf("paused update: %d: %s", code, body)
	}
	f.setState("")
	f.modes = map[string]string{"web_db": "global"}
	if code, body := e.scale("root", "web", "db", scaleRequest{Replicas: 2}); code != http.StatusConflict || !strings.Contains(body, "swarm_not_replicated") {
		t.Errorf("global service: %d: %s", code, body)
	}
	if len(f.actions) != 1 {
		t.Fatalf("a refused scale was sent: %+v", f.actions)
	}
}

func TestSwarmScaleStatefulWarning(t *testing.T) {
	e, f := newServiceEnv(t)
	db := "services:\n  db:\n    image: postgres:17\n    volumes:\n      - data:/var/lib/postgresql/data\nvolumes:\n  data: {}\n"
	if _, err := e.mgr.Create("pg", db, ""); err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.deploy("root", "pg", e.planHash(t, "pg")); code != http.StatusOK {
		t.Fatalf("deploy %d: %s", code, body)
	}
	if code, body := e.scale("root", "pg", "db", scaleRequest{Replicas: 2}); code != http.StatusConflict || !strings.Contains(body, "stateful_service") {
		t.Fatalf("stateful: %d: %s", code, body)
	}
	if code, body := e.scale("root", "pg", "db", scaleRequest{Replicas: 2, Force: true}); code != http.StatusOK {
		t.Fatalf("forced: %d: %s", code, body)
	}
	if len(f.actions) != 1 {
		t.Fatalf("actions %+v", f.actions)
	}
}

func TestSwarmScaleAccess(t *testing.T) {
	e, _ := newServiceEnv(t)
	e.role("op-c1", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksOperate}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("dep-c1", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("dep-local", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	for _, u := range []string{"op-c1", "dep-c1", "dep-local"} {
		e.user("u-"+u, u)
	}
	for user, want := range map[string]int{"u-op-c1": http.StatusForbidden, "u-dep-local": http.StatusForbidden, "u-dep-c1": http.StatusOK} {
		if code, body := e.scale(user, "web", "web", scaleRequest{Replicas: 2}); code != want {
			t.Errorf("%s: %d, want %d: %s", user, code, want, body)
		}
	}
}
