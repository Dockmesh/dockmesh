package stacks

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
)

func TestDeploymentTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := dbtest.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx := context.Background()
	s := NewDeploymentStore(database)
	if err := s.Set(ctx, "web", "agent-1", "deployed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTarget(ctx, "staged", "agent-2", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO stack_deployments (stack_name, target_kind, target_id, host_id) VALUES ('swarm', 'cluster', 'c1', '')`); err != nil {
		t.Fatal(err)
	}

	// A second start must leave rows written by the current code alone.
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		stack      string
		wantTarget Target
		wantHost   string
		wantErr    error
	}{
		{"web", HostTarget("agent-1"), "agent-1", nil},
		{"staged", HostTarget("agent-2"), "agent-2", nil},
		{"swarm", Target{Kind: TargetCluster, ID: "c1"}, "", ErrClusterTarget},
		{"unknown", Target{}, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.stack, func(t *testing.T) {
			got, err := s.TargetOf(ctx, tc.stack)
			if err != nil || got != tc.wantTarget {
				t.Errorf("TargetOf = %+v, %v; want %+v", got, err, tc.wantTarget)
			}
			hostID, err := s.HostOf(ctx, tc.stack)
			if !errors.Is(err, tc.wantErr) || hostID != tc.wantHost {
				t.Errorf("HostOf = %q, %v; want %q, %v", hostID, err, tc.wantHost, tc.wantErr)
			}
		})
	}

	d, err := s.Get(ctx, "web")
	if err != nil || d == nil || d.TargetKind != TargetHost || d.TargetID != "agent-1" || d.HostID != "agent-1" {
		t.Fatalf("Get(web) = %+v, %v", d, err)
	}
	onHost, err := s.ByHost(ctx, "")
	if err != nil || len(onHost) != 0 {
		t.Errorf("ByHost(\"\") = %v, %v; a cluster deployment is on no host", onHost, err)
	}
	if ok, err := s.MarkDeletePending(ctx, "swarm", "", false); err != nil || ok {
		t.Errorf("MarkDeletePending on a cluster target = %v, %v", ok, err)
	}
}
