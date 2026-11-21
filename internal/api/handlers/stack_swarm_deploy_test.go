package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// fakeSwarm records writes. With applied set, the service list shows the
// specs of the last deploy as running, whatever the deploy answered.
type fakeSwarm struct {
	mu      sync.Mutex
	deploys []agents.SwarmDeployReq
	hosts   []string
	err     error
	resp    *agents.SwarmDeployResp
	applied bool
}

func (f *fakeSwarm) SwarmDeploy(_ context.Context, hostID string, req agents.SwarmDeployReq) (agents.SwarmDeployResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deploys = append(f.deploys, req)
	f.hosts = append(f.hosts, hostID)
	if f.err != nil {
		return agents.SwarmDeployResp{}, f.err
	}
	if f.resp != nil {
		return *f.resp, nil
	}
	out := agents.SwarmDeployResp{}
	for _, n := range req.Networks {
		out.Networks = append(out.Networks, agents.SwarmWriteResult{Name: n.Name, ID: "n-" + n.Name, Action: agents.SwarmActionCreated})
	}
	for _, s := range req.Services {
		out.Services = append(out.Services, agents.SwarmWriteResult{Name: s.Spec.Name, ID: "s-" + s.Spec.Name, Action: agents.SwarmActionCreated})
	}
	return out, nil
}

func (f *fakeSwarm) SwarmServices(context.Context, string) (agents.SwarmServicesResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out agents.SwarmServicesResp
	if !f.applied || len(f.deploys) == 0 {
		return out, nil
	}
	last := f.deploys[len(f.deploys)-1]
	for _, s := range last.Services {
		out.Services = append(out.Services, agents.SwarmService{
			ID: "s-" + s.Spec.Name, Name: s.Spec.Name, Stack: last.Stack,
			SpecHash: s.Spec.Labels[agents.LabelSpecHash], RunningTasks: 1, DesiredTasks: 1,
		})
	}
	return out, nil
}

func (f *fakeSwarm) RegistryAuthFor(_ context.Context, _ string, images []string) (map[string]string, error) {
	out := map[string]string{}
	for _, img := range images {
		if strings.HasPrefix(img, "registry.example/") {
			out[img] = "blob"
		}
	}
	return out, nil
}

type swarmDeployEnv struct {
	*authzEnv
	mgr  *stacks.Manager
	fake *fakeSwarm
}

func newSwarmDeployEnv(t *testing.T) *swarmDeployEnv {
	t.Helper()
	e := newScopeEnv(t)
	e.h.Clusters = clusters.New(e.db, swarmHosts{})
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	mgr.SetSumStore(stacks.NewSumStore(e.db))
	e.h.Stacks = mgr
	e.h.Deployments = stacks.NewDeploymentStore(e.db)
	e.h.StackLocks = stacklock.New()
	e.h.Migrations = migration.NewService(e.db, host.NewFixedRegistry(), mgr, e.h.Deployments)
	fake := &fakeSwarm{applied: true}
	e.h.SwarmOps = fake
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.StackScope)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Get("/api/v1/stacks/{name}/swarm-preview", e.h.SwarmPreview)
		r.With(middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/api/v1/stacks/{name}/swarm-deploy", e.h.SwarmDeploy)
	})
	root := e.user("root", "admin")
	ctx := middleware.WithIdentity(context.Background(), middleware.Identity{UserID: root.ID, Access: e.h.Roles.AccessFor(root.ID)})
	if rr := clusterCall(ctx, e.h.CreateCluster, http.MethodPost, createClusterRequest{HostID: "local", Name: "prod"}); rr.Code != http.StatusCreated {
		t.Fatalf("create cluster %d: %s", rr.Code, rr.Body)
	}
	return &swarmDeployEnv{authzEnv: e, mgr: mgr, fake: fake}
}

func (e *swarmDeployEnv) planHash(t *testing.T, stack string) string {
	t.Helper()
	rr := e.do(http.MethodGet, "/api/v1/stacks/"+stack+"/swarm-preview?cluster=c1", e.login("root").AccessToken, nil)
	var p swarmPreviewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil || p.PlanHash == "" {
		t.Fatalf("preview %d: %s", rr.Code, rr.Body)
	}
	return p.PlanHash
}

func (e *swarmDeployEnv) deploy(user, stack, hash string) (int, swarmDeployResponse, string) {
	rr := e.do(http.MethodPost, "/api/v1/stacks/"+stack+"/swarm-deploy?cluster=c1", e.login(user).AccessToken, swarmDeployRequest{PlanHash: hash})
	var out swarmDeployResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out, rr.Body.String()
}

const webStack = "services:\n  web:\n    image: registry.example/web:1\n    environment:\n      TOKEN: ${TOKEN}\n  db:\n    image: postgres:17\n"

func TestSwarmDeployWrites(t *testing.T) {
	e := newSwarmDeployEnv(t)
	if _, err := e.mgr.Create("web", webStack, "TOKEN=s3cret\n"); err != nil {
		t.Fatal(err)
	}
	code, out, body := e.deploy("root", "web", e.planHash(t, "web"))
	if code != http.StatusOK || out.Outcome != swarmOutcomeApplied || out.Manager != "local" {
		t.Fatalf("%d: %s", code, body)
	}
	if len(e.fake.deploys) != 1 {
		t.Fatalf("%d deploys", len(e.fake.deploys))
	}
	req := e.fake.deploys[0]
	if len(req.Networks) != 1 || req.Networks[0].Labels[agents.LabelDockmeshStack] != "web" {
		t.Fatalf("networks %+v", req.Networks)
	}
	auth := map[string]string{}
	for _, s := range req.Services {
		if s.Spec.Labels[agents.LabelSpecHash] == "" {
			t.Fatalf("service %s without a spec hash", s.Spec.Name)
		}
		auth[s.Spec.Name] = s.RegistryAuth
	}
	if auth["web_web"] != "blob" || auth["web_db"] != "" {
		t.Fatalf("registry auth %v", auth)
	}
	for _, s := range out.Services {
		if s.RunningTasks != 1 || s.Applied != nil {
			t.Fatalf("service outcome %+v", s)
		}
	}
	d, err := e.h.Deployments.Get(context.Background(), "web")
	if err != nil || d == nil || d.TargetKind != stacks.TargetCluster || d.TargetID != "c1" || d.HostID != "" {
		t.Fatalf("deployment %+v, %v", d, err)
	}

	// The same files deploy again to the same cluster; a changed file
	// needs a new preview.
	if code, _, body := e.deploy("root", "web", e.planHash(t, "web")); code != http.StatusOK {
		t.Fatalf("redeploy %d: %s", code, body)
	}
	old := e.planHash(t, "web")
	if _, err := e.mgr.Update("web", strings.Replace(webStack, "postgres:17", "postgres:18", 1), "TOKEN=s3cret\n"); err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.deploy("root", "web", old); code != http.StatusConflict || !strings.Contains(body, "swarm_plan_changed") {
		t.Fatalf("stale plan %d: %s", code, body)
	}
}

func TestSwarmDeployRefusals(t *testing.T) {
	e := newSwarmDeployEnv(t)
	for name, src := range map[string]string{
		"onhost": "services:\n  web:\n    image: nginx\n",
		"bad":    "services:\n  web:\n    image: nginx\n    privileged: true\n",
		"named":  "services:\n  web:\n    image: nginx\n    networks: [n]\nnetworks:\n  n:\n    name: other_default\n",
	} {
		if _, err := e.mgr.Create(name, src, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.h.Deployments.Set(context.Background(), "onhost", "local", "deployed"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		stack, hash string
		want        int
		code        string
	}{
		{"onhost", "x", http.StatusConflict, "stack_other_target"},
		{"bad", "x", http.StatusUnprocessableEntity, "swarm_refused"},
		{"named", "x", http.StatusUnprocessableEntity, "swarm_refused"},
		{"missing", "x", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.stack, func(t *testing.T) {
			code, _, body := e.deploy("root", tt.stack, tt.hash)
			if code != tt.want || !strings.Contains(body, tt.code) {
				t.Fatalf("%d: %s", code, body)
			}
		})
	}
	if code, _, _ := e.deploy("root", "bad", ""); code != http.StatusBadRequest {
		t.Fatalf("no plan hash: %d", code)
	}
	if len(e.fake.deploys) != 0 {
		t.Fatalf("a refused deploy wrote: %+v", e.fake.deploys)
	}
}

// Deploy rights on the cluster are what counts; a host role on the manager
// or a cluster role without deploy is refused. A stack not deployed
// anywhere yet has no target, which a cluster role doesn't reach.
func TestSwarmDeployAccess(t *testing.T) {
	e := newSwarmDeployEnv(t)
	for _, name := range []string{"web", "fresh"} {
		if _, err := e.mgr.Create(name, webStack, "TOKEN=s3cret\n"); err != nil {
			t.Fatal(err)
		}
	}
	hash := e.planHash(t, "web")
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusOK {
		t.Fatalf("first deploy %d: %s", code, body)
	}
	e.role("deploy-c1", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("view-c1", []rbac.Perm{rbac.PermStacksView}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("deploy-local", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	e.role("deploy-c2", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c2"})
	for _, u := range []string{"deploy-c1", "view-c1", "deploy-local", "deploy-c2"} {
		e.user("u-"+u, u)
	}
	for user, want := range map[string]int{
		"u-view-c1":      http.StatusForbidden,
		"u-deploy-local": http.StatusForbidden,
		"u-deploy-c2":    http.StatusForbidden,
		"u-deploy-c1":    http.StatusOK,
	} {
		if code, _, body := e.deploy(user, "web", hash); code != want {
			t.Errorf("%s: %d, want %d: %s", user, code, want, body)
		}
	}
	if code, _, body := e.deploy("u-deploy-c1", "fresh", e.planHash(t, "fresh")); code != http.StatusForbidden {
		t.Errorf("cluster role on a stack without target: %d: %s", code, body)
	}
}

func TestSwarmDeployGuards(t *testing.T) {
	e := newSwarmDeployEnv(t)
	for _, name := range []string{"web", "app2"} {
		if _, err := e.mgr.Create(name, "services:\n  app:\n    image: nginx\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	hash := e.planHash(t, "web")

	release, err := e.h.StackLocks.TryLock(stacklock.ClusterKey("c1", "web"), stacklock.OpScale)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusConflict || !strings.Contains(body, "stack_busy") {
		t.Fatalf("locked: %d: %s", code, body)
	}
	release()

	release, err = e.h.StackLocks.Maintenance(context.Background(), []stacklock.Target{stacklock.Host("local")}, stacklock.OpMaintenance, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusConflict || !strings.Contains(body, "stack_busy") {
		t.Fatalf("manager in maintenance: %d: %s", code, body)
	}
	release()

	now := time.Now()
	if err := e.h.Migrations.Store().Create(context.Background(), &migration.Migration{
		ID: "m1", StackName: "web", SourceHostID: "local", TargetHostID: "a1", Status: migration.StatusSyncing, StartedAt: &now, InitiatedBy: "u",
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusConflict || !strings.Contains(body, "migration_active") {
		t.Fatalf("migrating: %d: %s", code, body)
	}
	if len(e.fake.deploys) != 0 {
		t.Fatalf("a refused deploy wrote: %+v", e.fake.deploys)
	}
}

// A write without answer is not sent again; the outcome comes from reading
// the cluster back.
func TestSwarmDeployUnknownOutcome(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		applied bool
		want    int
		outcome string
	}{
		{"applied after all", fmt.Errorf("%w: timed out", host.ErrSwarmWriteUnknown), true, http.StatusOK, swarmOutcomeApplied},
		{"not applied", fmt.Errorf("%w: agent gone", host.ErrSwarmWriteUnknown), false, http.StatusBadGateway, swarmOutcomeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSwarmDeployEnv(t)
			e.fake.err, e.fake.applied = tt.err, tt.applied
			if _, err := e.mgr.Create("web", webStack, "TOKEN=s3cret\n"); err != nil {
				t.Fatal(err)
			}
			code, out, body := e.deploy("root", "web", e.planHash(t, "web"))
			if code != tt.want || out.Outcome != tt.outcome || out.CheckedVia != "local" {
				t.Fatalf("%d: %s", code, body)
			}
			if len(e.fake.deploys) != 1 {
				t.Fatalf("write sent %d times", len(e.fake.deploys))
			}
			if len(out.Services) != 2 {
				t.Fatalf("services %+v", out.Services)
			}
			for _, s := range out.Services {
				if s.Applied == nil || *s.Applied != tt.applied {
					t.Fatalf("service %+v", s)
				}
			}
			d, _ := e.h.Deployments.Get(context.Background(), "web")
			if d == nil || d.TargetKind != stacks.TargetCluster {
				t.Fatalf("deployment %+v: want a cluster target recorded after a write that may have landed", d)
			}
		})
	}
}

func TestSwarmDeployPartialAndNotSent(t *testing.T) {
	e := newSwarmDeployEnv(t)
	if _, err := e.mgr.Create("web", webStack, "TOKEN=s3cret\n"); err != nil {
		t.Fatal(err)
	}
	e.fake.resp = &agents.SwarmDeployResp{
		Services: []agents.SwarmWriteResult{
			{Name: "web_db", ID: "s1", Action: agents.SwarmActionCreated},
			{Name: "web_web", Action: agents.SwarmActionFailed, Error: "no such image"},
		},
		Error: "web_web: no such image",
	}
	code, out, body := e.deploy("root", "web", e.planHash(t, "web"))
	if code != http.StatusBadGateway || out.Outcome != swarmOutcomePartial || !strings.Contains(out.Error, "no such image") {
		t.Fatalf("%d: %s", code, body)
	}

	e.fake.resp, e.fake.err = nil, agents.ErrNotSwarmManager
	if code, _, body := e.deploy("root", "web", e.planHash(t, "web")); code != http.StatusServiceUnavailable {
		t.Fatalf("not a manager: %d: %s", code, body)
	}
	if len(e.fake.deploys) != 2 {
		t.Fatalf("%d writes, want one per deploy", len(e.fake.deploys))
	}
}

// Dependencies must already run on the same cluster.
func TestSwarmDeployDependencies(t *testing.T) {
	e := newSwarmDeployEnv(t)
	e.h.Dependencies = stacks.NewDependencyStore(e.db)
	for _, name := range []string{"web", "db", "cache"} {
		if _, err := e.mgr.Create(name, "services:\n  app:\n    image: nginx\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := e.h.Dependencies.Set(ctx, "web", []string{"db"}); err != nil {
		t.Fatal(err)
	}
	hash := e.planHash(t, "web")
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusConflict || !strings.Contains(body, "not deployed to this cluster") {
		t.Fatalf("dependency nowhere: %d: %s", code, body)
	}
	if err := e.h.Deployments.Set(ctx, "db", "local", "deployed"); err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusConflict || !strings.Contains(body, "runs on host local") {
		t.Fatalf("dependency on a host: %d: %s", code, body)
	}
	if err := e.h.Deployments.SetCluster(ctx, "db", "c1", "deployed"); err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.deploy("root", "web", hash); code != http.StatusOK {
		t.Fatalf("dependency on the cluster: %d: %s", code, body)
	}
}

// A host deploy never pulls a dependency off its Swarm cluster.
func TestHostDeployRefusesClusterDependency(t *testing.T) {
	e := newSwarmDeployEnv(t)
	e.h.Dependencies = stacks.NewDependencyStore(e.db)
	local := &deployHost{recordingHost{id: "local"}}
	e.h.Hosts = host.NewFixedRegistry(local)
	e.router.With(middleware.NewAuth(e.h.Auth), middleware.RequirePerm(rbac.PermStacksDeploy)).Post("/hostdeploy/{name}", e.h.DeployStack)
	for _, name := range []string{"web", "db"} {
		if _, err := e.mgr.Create(name, "services:\n  app:\n    image: nginx\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := e.h.Dependencies.Set(ctx, "web", []string{"db"}); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Deployments.SetCluster(ctx, "db", "c1", "deployed"); err != nil {
		t.Fatal(err)
	}
	rr := e.do(http.MethodPost, "/hostdeploy/web", e.login("root").AccessToken, nil)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "runs on Swarm cluster c1") {
		t.Fatalf("%d: %s", rr.Code, rr.Body)
	}
}
