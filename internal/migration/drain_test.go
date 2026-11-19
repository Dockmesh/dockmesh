package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/system"
)

func TestDrainRefusesServerHost(t *testing.T) {
	e := newMigrationEnv(t)
	drains := NewDrainService(e.svc, e.db)
	for _, id := range []string{"local", ""} {
		if _, err := drains.Plan(context.Background(), id); !errors.Is(err, ErrDrainServerHost) {
			t.Errorf("plan %q: err = %v, want ErrDrainServerHost", id, err)
		}
		if _, err := drains.Execute(context.Background(), id, "u1", nil); !errors.Is(err, ErrDrainServerHost) {
			t.Errorf("execute %q: err = %v, want ErrDrainServerHost", id, err)
		}
	}
	if e.source.stops != 0 {
		t.Fatal("a refused drain stopped a stack")
	}
}

// A drain that pauses on its last stack ends as the user decides: Abort
// records aborted, Resume completed.
func TestDrainPausedOnLastStack(t *testing.T) {
	saved := drainPoll
	drainPoll = 10 * time.Millisecond
	t.Cleanup(func() { drainPoll = saved })

	tests := []struct {
		name   string
		decide func(*DrainOrchestrator)
		want   string
	}{
		{"abort", (*DrainOrchestrator).Abort, DrainAborted},
		{"resume", (*DrainOrchestrator).Resume, DrainCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMigrationEnv(t)
			e.target.deployErr = errors.New("boom")
			store := NewDrainStore(e.db)
			d := &Drain{ID: "d1", SourceHostID: "agent0", Status: DrainPlanning,
				Plan: []PlanEntry{{StackName: "web", TargetHostID: "agent1", Feasible: true}}}
			if err := store.Create(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			orch := NewDrainOrchestrator(e.svc, store)
			done := make(chan struct{})
			go func() {
				orch.Execute(context.Background(), d, "u1", nil)
				close(done)
			}()
			waitDrainStatus(t, store, "d1", DrainPaused)
			tt.decide(orch)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("drain did not end")
			}
			got, err := store.Get(context.Background(), "d1")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.want || got.CompletedAt == nil {
				t.Fatalf("status = %s (completed_at %v), want %s", got.Status, got.CompletedAt, tt.want)
			}
		})
	}
}

func waitDrainStatus(t *testing.T, store *DrainStore, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		d, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("drain never reached %s", want)
}

// A finished migration leaves one deployment row, on the target; the
// source host is in the migration record.
func TestMigrationCommitKeepsOneDeploymentRow(t *testing.T) {
	e := newMigrationEnv(t)
	m := e.migrate(t)
	if m.Status != StatusCompleted || m.SourceHostID != "agent0" {
		t.Fatalf("migration = %s from %s", m.Status, m.SourceHostID)
	}
	var rows []string
	r, err := e.db.Query(`SELECT stack_name || '@' || host_id || '/' || status FROM stack_deployments`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for r.Next() {
		var s string
		if err := r.Scan(&s); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, s)
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0] != "web@agent1/deployed" {
		t.Fatalf("stack_deployments = %v, want [web@agent1/deployed]", rows)
	}
}

// A stack with named volumes can't move, so the plan lists it as a
// blocker with the reason and the drain is refused.
func TestDrainPlanBlocksNamedVolumes(t *testing.T) {
	e := newMigrationEnv(t)
	e.target.metrics = system.Metrics{DiskTotal: 100 << 30}
	if _, err := e.mgr.Create("db", "services:\n  db:\n    image: postgres\n    volumes: [data:/var/lib/postgresql/data]\nvolumes:\n  data: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.deployments.Set(context.Background(), "db", "agent0", "deployed"); err != nil {
		t.Fatal(err)
	}
	drains := NewDrainService(e.svc, e.db)
	plan, err := drains.Plan(context.Background(), "agent0")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Feasible {
		t.Fatal("plan with a named-volume stack is feasible")
	}
	got := map[string]PlanEntry{}
	for _, en := range plan.Entries {
		got[en.StackName] = en
	}
	if db := got["db"]; db.Feasible || db.TargetHostID != "" || !strings.Contains(db.Detail, "named volumes") || !strings.Contains(db.Detail, "db_data") {
		t.Fatalf("db entry = %+v", db)
	}
	if web := got["web"]; !web.Feasible || web.TargetHostID != "agent1" {
		t.Fatalf("web entry = %+v", web)
	}
	_, err = drains.Execute(context.Background(), "agent0", "u1", nil)
	var blocked *DrainBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("execute: err = %v, want DrainBlockedError", err)
	}
	if e.source.stops != 0 {
		t.Fatal("a blocked drain stopped a stack")
	}
}
