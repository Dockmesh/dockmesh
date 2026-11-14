package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dockmesh/dockmesh/internal/system"
)

// setHooks writes migration hooks into the stack's meta file, keeping
// what else it holds.
func (e *migrationEnv) setHooks(t *testing.T, pre, post string) {
	t.Helper()
	dir, err := e.mgr.Dir("web")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".dockmesh.meta.json")
	meta := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &meta); err != nil {
			t.Fatal(err)
		}
	}
	meta["migration"] = map[string]string{"pre_dump": pre, "post_restore": post}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.AcceptExternal(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
}

func TestInitiateNeedsHookConfirmation(t *testing.T) {
	e := newMigrationEnv(t)
	e.setHooks(t, "pg_dump -f /dump.sql", "psql -f /dump.sql")
	pre, err := e.svc.Preflight(context.Background(), "web", "agent1")
	if err != nil {
		t.Fatal(err)
	}
	if pre.Hooks == nil || pre.Hooks.PreDump != "pg_dump -f /dump.sql" || pre.Hooks.PostRestore != "psql -f /dump.sql" || pre.Hooks.Digest == "" {
		t.Fatalf("preflight hooks = %+v", pre.Hooks)
	}

	for _, confirm := range []string{"", "0000"} {
		_, err := e.svc.Initiate(context.Background(), "web", "agent1", "u1", confirm)
		var unconfirmed *HooksUnconfirmedError
		if !errors.As(err, &unconfirmed) || !errors.Is(err, ErrHooksUnconfirmed) || unconfirmed.Hooks.Digest != pre.Hooks.Digest {
			t.Fatalf("confirm %q: err = %v", confirm, err)
		}
	}
	if len(e.source.execs) != 0 || e.source.stops != 0 {
		t.Fatal("an unconfirmed migration ran something")
	}

	e.setHooks(t, "pg_dump -f /other.sql", "psql -f /dump.sql")
	if _, err := e.svc.Initiate(context.Background(), "web", "agent1", "u1", pre.Hooks.Digest); !errors.Is(err, ErrHooksUnconfirmed) {
		t.Fatalf("hooks changed after the preflight: err = %v", err)
	}
	e.setHooks(t, "pg_dump -f /dump.sql", "psql -f /dump.sql")

	m := e.migrateConfirmed(t, pre.Hooks.Digest)
	if m.Status != StatusCompleted {
		t.Fatalf("status %s: %s", m.Status, m.ErrorMessage)
	}
	if want := []string{"local-web: sh -c pg_dump -f /dump.sql"}; !reflect.DeepEqual(e.source.execs, want) {
		t.Errorf("source ran %q, want %q", e.source.execs, want)
	}
	if want := []string{"agent1-web: sh -c psql -f /dump.sql"}; !reflect.DeepEqual(e.target.execs, want) {
		t.Errorf("target ran %q, want %q", e.target.execs, want)
	}
}

func TestDrainNeedsHookConfirmation(t *testing.T) {
	e := newMigrationEnv(t)
	e.target.metrics = system.Metrics{DiskTotal: 100 << 30}
	e.setHooks(t, "pg_dump -f /dump.sql", "")
	drains := NewDrainService(e.svc, e.svc.db)

	plan, err := drains.Plan(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Hooks == nil || plan.Entries[0].Hooks.PreDump != "pg_dump -f /dump.sql" {
		t.Fatalf("plan = %+v", plan.Entries)
	}
	digest := plan.Entries[0].Hooks.Digest

	for _, confirm := range []map[string]string{nil, {"web": "nope"}, {"other": digest}} {
		_, err := drains.Execute(context.Background(), "local", "u1", confirm)
		var unconfirmed *DrainHooksUnconfirmedError
		if !errors.As(err, &unconfirmed) || len(unconfirmed.Entries) != 1 || unconfirmed.Entries[0].StackName != "web" {
			t.Fatalf("confirm %v: err = %v", confirm, err)
		}
	}
	d, err := drains.Execute(context.Background(), "local", "u1", map[string]string{"web": digest})
	if err != nil {
		t.Fatalf("confirmed drain: %v", err)
	}
	_ = drains.AbortDrain(d.ID)
}
