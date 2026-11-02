package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type gitFakeHost struct {
	host.Host
	id      string
	deploys []string
}

func (f *gitFakeHost) ID() string { return f.id }
func (f *gitFakeHost) DeployStack(_ context.Context, _, composeYAML, env string) (*compose.DeployResult, error) {
	f.deploys = append(f.deploys, composeYAML+"\n"+env)
	return &compose.DeployResult{}, nil
}

type gitFakeHosts map[string]*gitFakeHost

func (h gitFakeHosts) Pick(id string) (host.Host, error) {
	if f, ok := h[id]; ok {
		return f, nil
	}
	return nil, host.ErrAgentOffline
}

func TestGitDeployTargetsStackHost(t *testing.T) {
	tests := []struct {
		name     string
		deployed string // deployment row host, "" = none
		target   string // creation target, "" = none
		wantHost string
		wantErr  bool
	}{
		{name: "deployed on agent", deployed: "agent1", wantHost: "agent1"},
		{name: "created for agent", target: "agent1", wantHost: "agent1"},
		{name: "no host recorded", wantHost: "local"},
		{name: "agent offline", deployed: "agent2", wantHost: "agent2", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
			mgr, err := stacks.NewManager(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Close()
			if _, err := mgr.Create("web", "services:\n  web:\n    image: nginx\n", "A=1\n"); err != nil {
				t.Fatal(err)
			}
			deps := stacks.NewDeploymentStore(database)
			if tt.deployed != "" {
				if err := deps.Set(ctx, "web", tt.deployed, "deployed"); err != nil {
					t.Fatal(err)
				}
			}
			if tt.target != "" {
				if err := deps.SetTarget(ctx, "web", tt.target, ""); err != nil {
					t.Fatal(err)
				}
			}
			hosts := gitFakeHosts{"local": {id: "local"}, "agent1": {id: "agent1"}}
			history := stacks.NewHistoryStore(database)
			g := &gitDeployer{
				stacks: mgr, deployments: deps, history: history, hosts: hosts,
				globals: func(context.Context) ([]globalenv.Var, error) {
					return []globalenv.Var{{Key: "GLOBAL", Value: "g"}}, nil
				},
			}
			_, err = g.deploy(ctx, "web")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			for id, h := range hosts {
				want := 0
				if id == tt.wantHost && !tt.wantErr {
					want = 1
				}
				if len(h.deploys) != want {
					t.Errorf("deploys on %s = %d, want %d", id, len(h.deploys), want)
				}
				for _, d := range h.deploys {
					if !strings.Contains(d, "GLOBAL=g") {
						t.Errorf("deploy on %s without global env: %q", id, d)
					}
				}
			}
			dep, err := deps.Get(ctx, "web")
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tt.wantErr && tt.deployed == "" && dep != nil:
				t.Errorf("deployment row written for a failed deploy: %+v", dep)
			case !tt.wantErr && (dep == nil || dep.HostID != tt.wantHost):
				t.Errorf("deployment row = %+v, want host %s", dep, tt.wantHost)
			}
			entries, err := history.List(ctx, "web", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].HostID != tt.wantHost {
				t.Fatalf("history = %+v, want one entry on %s", entries, tt.wantHost)
			}
		})
	}
}
