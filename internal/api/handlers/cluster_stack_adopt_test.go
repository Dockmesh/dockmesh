package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// adoptSwarm is fakeSwarm with a stack the CLI deployed.
type adoptSwarm struct {
	*fakeSwarm
	services []swarm.Service
	networks []network.Summary
	adopts   []agents.SwarmAdoptReq
	adoptErr error
}

func (f *adoptSwarm) SwarmExport(_ context.Context, _ string, req agents.SwarmExportReq) (agents.SwarmExportResp, error) {
	var out agents.SwarmExportResp
	for _, s := range f.services {
		if s.Spec.Labels[agents.LabelStackNamespace] == req.Stack {
			out.Services = append(out.Services, s)
		}
	}
	if len(out.Services) == 0 {
		return out, agents.ErrSwarmWriteRefused
	}
	out.Networks = f.networks
	return out, nil
}

func (f *adoptSwarm) SwarmAdopt(_ context.Context, _ string, req agents.SwarmAdoptReq) (agents.SwarmAdoptResp, error) {
	f.adopts = append(f.adopts, req)
	if f.adoptErr != nil {
		return agents.SwarmAdoptResp{}, f.adoptErr
	}
	out := agents.SwarmAdoptResp{}
	for _, s := range req.Services {
		out.Services = append(out.Services, agents.SwarmWriteResult{Name: s.Name, ID: s.ID, Action: agents.SwarmActionUpdated})
	}
	return out, nil
}

func cliStack() ([]swarm.Service, []network.Summary) {
	ns := map[string]string{agents.LabelStackNamespace: "shop"}
	net := network.Summary{Network: network.Network{ID: "net1", Name: "shop_default", Scope: "swarm", Driver: "overlay", Labels: ns}}
	one := uint64(2)
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: "shop_web", Labels: map[string]string{agents.LabelStackNamespace: "shop", "com.docker.stack.image": "nginx:1.29"}},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:  "nginx:1.29@sha256:" + strings.Repeat("b", 64),
				Labels: map[string]string{agents.LabelStackNamespace: "shop"},
				Env:    []string{"API_KEY=hunter2"},
				User:   "nobody",
			},
			Networks:  []swarm.NetworkAttachmentConfig{{Target: "net1", Aliases: []string{"web"}}},
			Placement: &swarm.Placement{},
			Resources: &swarm.ResourceRequirements{},
		},
		Mode:         swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &one}},
		EndpointSpec: &swarm.EndpointSpec{Mode: swarm.ResolutionModeVIP},
	}
	svc := swarm.Service{ID: "svc1", Spec: spec}
	svc.Version.Index = 7
	return []swarm.Service{svc}, []network.Summary{net}
}

func newAdoptEnv(t *testing.T) (*swarmDeployEnv, *adoptSwarm) {
	t.Helper()
	e := newSwarmDeployEnv(t)
	svcs, nets := cliStack()
	f := &adoptSwarm{fakeSwarm: e.fake, services: svcs, networks: nets}
	e.h.SwarmOps = f
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(e.h.ClusterScope)
		r.Use(middleware.RequirePerm(rbac.PermStacksAdopt))
		r.Get("/api/v1/clusters/{id}/stacks/{stack}/adopt", e.h.SwarmAdoptPreview)
		r.Post("/api/v1/clusters/{id}/stacks/{stack}/adopt", e.h.SwarmAdopt)
	})
	return e, f
}

func (e *swarmDeployEnv) adoptPreview(t *testing.T, user, stack string) (int, adoptPreview, string) {
	t.Helper()
	rr := e.do(http.MethodGet, "/api/v1/clusters/c1/stacks/"+stack+"/adopt", e.login(user).AccessToken, nil)
	var p adoptPreview
	_ = json.Unmarshal(rr.Body.Bytes(), &p)
	return rr.Code, p, rr.Body.String()
}

func (e *swarmDeployEnv) adopt(user, stack, hash string) (int, string) {
	rr := e.do(http.MethodPost, "/api/v1/clusters/c1/stacks/"+stack+"/adopt", e.login(user).AccessToken, swarmAdoptRequest{PlanHash: hash})
	return rr.Code, rr.Body.String()
}

func TestSwarmAdopt(t *testing.T) {
	e, f := newAdoptEnv(t)
	code, p, body := e.adoptPreview(t, "root", "shop")
	if code != http.StatusOK {
		t.Fatalf("preview %d: %s", code, body)
	}
	if strings.Contains(body, "hunter2") {
		t.Fatalf("an environment value in the preview: %s", body)
	}
	if len(p.EnvKeys) != 1 || p.EnvKeys[0] != "WEB_API_KEY" {
		t.Fatalf("env keys %v", p.EnvKeys)
	}
	if len(p.Services) != 1 || strings.Join(p.Services[0].Changes, ",") != "TaskTemplate.ContainerSpec.User" {
		t.Fatalf("services %+v", p.Services)
	}
	if len(p.KeptNetworks) != 1 || p.KeptNetworks[0] != "shop_default" {
		t.Fatalf("kept networks %v", p.KeptNetworks)
	}
	if len(f.adopts) != 0 || e.mgr.Has("shop") {
		t.Fatal("the preview wrote something")
	}

	code, body = e.adopt("root", "shop", p.PlanHash)
	if code != http.StatusCreated {
		t.Fatalf("adopt %d: %s", code, body)
	}
	if len(f.adopts) != 1 || len(f.adopts[0].Services) != 1 {
		t.Fatalf("adopt writes %+v", f.adopts)
	}
	lab := f.adopts[0].Services[0]
	if lab.ID != "svc1" || lab.Version != 7 {
		t.Fatalf("label write %+v", lab)
	}
	d, err := e.mgr.Get("shop")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.Compose, "hunter2") || !strings.Contains(d.Env, "WEB_API_KEY=hunter2") {
		t.Fatalf("files: compose %q env %q", d.Compose, d.Env)
	}
	dep, err := e.h.Deployments.Get(context.Background(), "shop")
	if err != nil || dep == nil || dep.TargetKind != stacks.TargetCluster || dep.TargetID != "c1" {
		t.Fatalf("deployment %+v %v", dep, err)
	}
	// The label is the hash the next deploy writes for the same file.
	pv := e.preview(t, "shop")
	if len(pv.Services) == 0 {
		t.Fatalf("deploy preview of the adopted stack: %+v", pv)
	}
	if code, _, body := e.adoptPreview(t, "root", "shop"); code != http.StatusConflict {
		t.Fatalf("adopt of a Dockmesh stack: %d: %s", code, body)
	}
}

func TestSwarmAdoptLabelMatchesDeploy(t *testing.T) {
	e, f := newAdoptEnv(t)
	_, p, _ := e.adoptPreview(t, "root", "shop")
	if code, body := e.adopt("root", "shop", p.PlanHash); code != http.StatusCreated {
		t.Fatalf("adopt %d: %s", code, body)
	}
	code, out, body := e.deploy("root", "shop", e.planHash(t, "shop"))
	if code != http.StatusOK {
		t.Fatalf("deploy %d: %s %+v", code, body, out)
	}
	dep := e.fake.deploys[len(e.fake.deploys)-1]
	if len(dep.Networks) != 0 || len(dep.ExternalNetworks) != 1 || dep.ExternalNetworks[0] != "shop_default" {
		t.Fatalf("an adopted stack's network must stay external: %+v %v", dep.Networks, dep.ExternalNetworks)
	}
	if got := dep.Services[0].Spec.Labels[agents.LabelSpecHash]; got != f.adopts[0].Services[0].SpecHash {
		t.Fatalf("deploy hash %s, adopt label %s", got, f.adopts[0].Services[0].SpecHash)
	}
}

func TestSwarmAdoptRefusals(t *testing.T) {
	e, f := newAdoptEnv(t)
	_, p, _ := e.adoptPreview(t, "root", "shop")
	f.services[0].Version.Index = 8
	if code, body := e.adopt("root", "shop", p.PlanHash); code != http.StatusConflict || !strings.Contains(body, "swarm_plan_changed") {
		t.Fatalf("changed since the preview: %d: %s", code, body)
	}
	if code, _, body := e.adoptPreview(t, "root", "other"); code != http.StatusConflict {
		t.Fatalf("stack the cluster doesn't run: %d: %s", code, body)
	}
	if _, err := e.mgr.Create("taken", webStack, ""); err != nil {
		t.Fatal(err)
	}
	if code, _, body := e.adoptPreview(t, "root", "taken"); code != http.StatusConflict || !strings.Contains(body, "stack_exists") {
		t.Fatalf("name taken: %d: %s", code, body)
	}
	_, p, _ = e.adoptPreview(t, "root", "shop")
	f.adoptErr = agents.ErrSwarmWriteRefused
	if code, body := e.adopt("root", "shop", p.PlanHash); code != http.StatusConflict {
		t.Fatalf("refused label write: %d: %s", code, body)
	}
	if e.mgr.Has("shop") {
		t.Fatal("stack files written after a refused label write")
	}
}

func TestSwarmAdoptAccess(t *testing.T) {
	e, _ := newAdoptEnv(t)
	e.role("adopt-c1", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksAdopt}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("deploy-c1", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksDeploy}, rbac.Scope{Type: rbac.ScopeCluster, Host: "c1"})
	e.role("adopt-local", []rbac.Perm{rbac.PermStacksView, rbac.PermStacksAdopt}, rbac.Scope{Type: rbac.ScopeHost, Host: "local"})
	for _, u := range []string{"adopt-c1", "deploy-c1", "adopt-local"} {
		e.user("u-"+u, u)
	}
	for user, want := range map[string]int{
		"u-deploy-c1":   http.StatusForbidden,
		"u-adopt-local": http.StatusForbidden,
		"u-adopt-c1":    http.StatusOK,
	} {
		if code, _, body := e.adoptPreview(t, user, "shop"); code != want {
			t.Errorf("%s: %d, want %d: %s", user, code, want, body)
		}
	}
	_, p, _ := e.adoptPreview(t, "u-adopt-c1", "shop")
	if code, body := e.adopt("u-adopt-c1", "shop", p.PlanHash); code != http.StatusCreated {
		t.Fatalf("adopt with a cluster role: %d: %s", code, body)
	}
}
