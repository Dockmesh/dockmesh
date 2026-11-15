package stacks

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db/dbtest"
)

func TestBackfillDeployments(t *testing.T) {
	local := func(project string) ContainerInfo {
		return ContainerInfo{Labels: map[string]string{"com.docker.compose.project": project}, HostID: "local"}
	}
	tests := []struct {
		name     string
		target   string // host the stack was created for, "" for none
		deployed string // existing deployment host, "" for none
		wantHost string // "" = no deployment row
	}{
		{name: "running stack without a row is filled in", wantHost: "local"},
		{name: "created for this host", target: "local", wantHost: "local"},
		{name: "created for an agent, old local containers", target: "agent-1"},
		{name: "deployed elsewhere already", deployed: "agent-1", wantHost: "agent-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mgr, err := NewManager(t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.Create("app", "services:\n  web:\n    image: nginx\n", ""); err != nil {
				t.Fatal(err)
			}
			ds := NewDeploymentStore(database)
			if tt.target != "" {
				if err := ds.SetTarget(ctx, "app", tt.target, ""); err != nil {
					t.Fatal(err)
				}
			}
			if tt.deployed != "" {
				if err := ds.Set(ctx, "app", tt.deployed, "deployed"); err != nil {
					t.Fatal(err)
				}
			}
			for start := 1; start <= 2; start++ {
				if err := BackfillDeployments(ctx, ds, mgr, []ContainerInfo{local("app"), local("not-a-stack")}); err != nil {
					t.Fatal(err)
				}
				d, err := ds.Get(ctx, "app")
				if err != nil {
					t.Fatal(err)
				}
				got := ""
				if d != nil {
					got = d.HostID
				}
				if got != tt.wantHost {
					t.Fatalf("start %d: deployed on %q, want %q", start, got, tt.wantHost)
				}
				if host, err := ds.Target(ctx, "app"); err != nil || host != tt.target {
					t.Fatalf("start %d: target %q (%v), want %q", start, host, err, tt.target)
				}
			}
		})
	}
}
