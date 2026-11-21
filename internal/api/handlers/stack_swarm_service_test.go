package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// serviceSwarm is fakeSwarm whose services report an update state.
type serviceSwarm struct {
	*fakeSwarm
	smu     sync.Mutex
	state   string
	actions []agents.SwarmServiceActionReq
	// modes overrides the mode of a service by name; the others run
	// replicated with one task.
	modes map[string]string
}

func (f *serviceSwarm) setState(s string) {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.state = s
}

func (f *serviceSwarm) SwarmServices(ctx context.Context, h string) (agents.SwarmServicesResp, error) {
	out, err := f.fakeSwarm.SwarmServices(ctx, h)
	f.smu.Lock()
	defer f.smu.Unlock()
	one := uint64(1)
	for i := range out.Services {
		out.Services[i].UpdateState = f.state
		out.Services[i].Mode, out.Services[i].Replicas = "replicated", &one
		if m, ok := f.modes[out.Services[i].Name]; ok {
			out.Services[i].Mode, out.Services[i].Replicas = m, nil
		}
	}
	return out, err
}

func (f *serviceSwarm) SwarmServiceAction(_ context.Context, _ string, req agents.SwarmServiceActionReq) (agents.SwarmWriteResult, error) {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.actions = append(f.actions, req)
	return agents.SwarmWriteResult{Name: req.Service, ID: req.ID, Action: agents.SwarmActionUpdated}, nil
}

func newServiceEnv(t *testing.T) (*swarmDeployEnv, *serviceSwarm) {
	t.Helper()
	e := newSwarmDeployEnv(t)
	f := &serviceSwarm{fakeSwarm: e.fake}
	e.h.SwarmOps = f
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermStacksOperate)).Post("/api/v1/stacks/{name}/swarm-services/{service}/rollback", e.h.SwarmServiceRollback)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/api/v1/stacks/{name}/swarm-services/{service}/resume", e.h.SwarmServiceResume)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/api/v1/stacks/{name}/services/{service}/scale", e.h.ScaleService)
	})
	if _, err := e.mgr.Create("web", webStack, "TOKEN=s3cret\n"); err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.deploy("root", "web", e.planHash(t, "web")); code != http.StatusOK {
		t.Fatalf("deploy %d: %s", code, body)
	}
	return e, f
}

func (e *swarmDeployEnv) serviceAction(user, service, action string) (int, string) {
	rr := e.do(http.MethodPost, "/api/v1/stacks/web/swarm-services/"+service+"/"+action, e.login(user).AccessToken, nil)
	return rr.Code, rr.Body.String()
}

func TestSwarmServiceActions(t *testing.T) {
	e, f := newServiceEnv(t)
	f.setState("paused")
	code, body := e.serviceAction("root", "web", "resume")
	if code != http.StatusOK {
		t.Fatalf("resume %d: %s", code, body)
	}
	var out swarmServiceActionResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Action != agents.SwarmActionUpdated || out.ClusterID != "c1" {
		t.Fatalf("resume answer %s", body)
	}
	if code, body := e.serviceAction("root", "db", "rollback"); code != http.StatusOK {
		t.Fatalf("rollback %d: %s", code, body)
	}
	if len(f.actions) != 2 || f.actions[0].Service != "web_web" || f.actions[0].Action != agents.SwarmServiceResume ||
		f.actions[1].Service != "web_db" || f.actions[1].Action != agents.SwarmServiceRollback || f.actions[1].ID != "s-web_db" {
		t.Fatalf("actions %+v", f.actions)
	}
	if code, _ := e.serviceAction("root", "nope", "rollback"); code != http.StatusNotFound {
		t.Fatalf("unknown service: %d", code)
	}
	if _, err := e.mgr.Create("onhost", webStack, ""); err != nil {
		t.Fatal(err)
	}
	rr := e.do(http.MethodPost, "/api/v1/stacks/onhost/swarm-services/web/rollback", e.login("root").AccessToken, nil)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "stack_not_on_cluster") {
		t.Fatalf("stack without a cluster: %d: %s", rr.Code, rr.Body)
	}
}

// Operators may roll back but not resume; deployers do both.
func TestSwarmServiceActionAccess(t *testing.T) {
	e, f := newServiceEnv(t)
	f.setState("paused")
	e.role("op-c1", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksOperate}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("dep-c1", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("op-local", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksOperate, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	for _, u := range []string{"op-c1", "dep-c1", "op-local"} {
		e.user("u-"+u, u)
	}
	for _, tc := range []struct {
		user, action string
		want         int
	}{
		{"u-op-c1", "rollback", http.StatusOK},
		{"u-op-c1", "resume", http.StatusForbidden},
		{"u-dep-c1", "resume", http.StatusOK},
		{"u-op-local", "rollback", http.StatusForbidden},
		{"u-op-local", "resume", http.StatusForbidden},
	} {
		if code, body := e.serviceAction(tc.user, "web", tc.action); code != tc.want {
			t.Errorf("%s %s: %d, want %d: %s", tc.user, tc.action, code, tc.want, body)
		}
	}
}

// While Swarm rolls an update out, the stack's cluster lock stays held
// for deploys and resumes; a rollback ends it and a paused update frees it.
func TestSwarmUpdateHoldsTheLock(t *testing.T) {
	oldPoll := swarmUpdatePoll
	swarmUpdatePoll = 10 * time.Millisecond
	t.Cleanup(func() { swarmUpdatePoll = oldPoll })
	e, f := newServiceEnv(t)
	f.setState("updating")
	hash := e.planHash(t, "web")
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusOK {
		t.Fatalf("deploy %d: %s", code, body)
	}
	if code, body := e.serviceAction("root", "web", "resume"); code != http.StatusConflict || !strings.Contains(body, "Swarm rolling update") {
		t.Fatalf("resume while rolling out: %d: %s", code, body)
	}
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusConflict {
		t.Fatalf("deploy while rolling out: %d: %s", code, body)
	}
	// A rollback that writes nothing leaves the rollout held.
	if code, _ := e.serviceAction("root", "nope", "rollback"); code != http.StatusNotFound {
		t.Fatalf("rollback of an unknown service: %d", code)
	}
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusConflict {
		t.Fatalf("deploy after a refused rollback: %d: %s", code, body)
	}
	// A rollback ends the hold: it is what resolves a bad rollout.
	if code, body := e.serviceAction("root", "web", "rollback"); code != http.StatusOK {
		t.Fatalf("rollback while rolling out: %d: %s", code, body)
	}
	f.setState("paused")
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := e.serviceAction("root", "web", "rollback")
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not freed after the update paused: %d: %s", code, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
