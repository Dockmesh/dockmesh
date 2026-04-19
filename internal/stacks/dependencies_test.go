package stacks

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func setupDepTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE stack_dependencies (
			stack_name TEXT NOT NULL,
			depends_on TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (stack_name, depends_on),
			CHECK (stack_name <> depends_on)
		)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDependencyStore_SetAndGet(t *testing.T) {
	ctx := context.Background()
	s := NewDependencyStore(setupDepTestDB(t))

	if err := s.Set(ctx, "api", []string{"postgres", "redis"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Get(ctx, "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "postgres" || got[1] != "redis" {
		t.Fatalf("got %v want [postgres redis]", got)
	}

	// Replacing drops old edges.
	if err := s.Set(ctx, "api", []string{"mongo"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx, "api")
	if len(got) != 1 || got[0] != "mongo" {
		t.Fatalf("replacement: got %v", got)
	}
}

func TestDependencyStore_SetDedupAndSelfRef(t *testing.T) {
	ctx := context.Background()
	s := NewDependencyStore(setupDepTestDB(t))

	if err := s.Set(ctx, "api", []string{"api", "pg", "pg", ""}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "api")
	if len(got) != 1 || got[0] != "pg" {
		t.Fatalf("dedup/self-ref: got %v want [pg]", got)
	}
}

func TestDependencyStore_CycleDirect(t *testing.T) {
	ctx := context.Background()
	s := NewDependencyStore(setupDepTestDB(t))

	if err := s.Set(ctx, "a", []string{"b"}); err != nil {
		t.Fatal(err)
	}
	err := s.Set(ctx, "b", []string{"a"})
	if !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("want ErrDependencyCycle, got %v", err)
	}
}

func TestDependencyStore_CycleTransitive(t *testing.T) {
	ctx := context.Background()
	s := NewDependencyStore(setupDepTestDB(t))

	if err := s.Set(ctx, "a", []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "b", []string{"c"}); err != nil {
		t.Fatal(err)
	}
	// c -> a would close a -> b -> c -> a.
	err := s.Set(ctx, "c", []string{"a"})
	if !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("want cycle, got %v", err)
	}
}

func TestDependencyStore_TopoOrder(t *testing.T) {
	ctx := context.Background()
	s := NewDependencyStore(setupDepTestDB(t))

	// api -> postgres, redis
	// postgres -> consul
	if err := s.Set(ctx, "api", []string{"postgres", "redis"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "postgres", []string{"consul"}); err != nil {
		t.Fatal(err)
	}

	order, err := s.TopoOrder(ctx, "api")
	if err != nil {
		t.Fatal(err)
	}
	// Must end with api; consul must come before postgres; postgres/redis
	// before api. Alphabetical within a level means: consul, postgres, redis, api.
	want := []string{"consul", "postgres", "redis", "api"}
	if len(order) != len(want) {
		t.Fatalf("len: got %v want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("got %v want %v", order, want)
		}
	}
}

func TestDependencyStore_Dependents(t *testing.T) {
	ctx := context.Background()
	s := NewDependencyStore(setupDepTestDB(t))
	_ = s.Set(ctx, "api", []string{"postgres"})
	_ = s.Set(ctx, "worker", []string{"postgres"})
	got, err := s.Dependents(ctx, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "api" || got[1] != "worker" {
		t.Fatalf("got %v want [api worker]", got)
	}
}

func TestDependencyStore_DeleteAll(t *testing.T) {
	ctx := context.Background()
	s := NewDependencyStore(setupDepTestDB(t))
	_ = s.Set(ctx, "api", []string{"pg"})
	_ = s.Set(ctx, "worker", []string{"pg"})
	if err := s.DeleteAll(ctx, "pg"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "api"); len(got) != 0 {
		t.Fatalf("want api deps gone, got %v", got)
	}
	if got, _ := s.Dependents(ctx, "pg"); len(got) != 0 {
		t.Fatalf("want pg dependents gone, got %v", got)
	}
}
