package migration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/scaling"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/system"
)

type fakeHost struct {
	host.Host
	id        string
	stopErr   error
	deployErr error

	mu       sync.Mutex
	stops    int
	deploys  []string // compose + "\n---\n" + env per deploy
	deployed bool
}

func (f *fakeHost) ID() string   { return f.id }
func (f *fakeHost) Name() string { return f.id }
func (f *fakeHost) SystemMetrics(context.Context) (system.Metrics, error) {
	return system.Metrics{}, nil
}
func (f *fakeHost) ListImages(context.Context, bool) ([]image.Summary, error) { return nil, nil }
func (f *fakeHost) StopStack(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	if f.stopErr == nil {
		f.deployed = false
	}
	return f.stopErr
}
func (f *fakeHost) DeployStack(_ context.Context, _, composeYAML, env string) (*compose.DeployResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deploys = append(f.deploys, composeYAML+"\n---\n"+env)
	if f.deployErr != nil {
		return nil, f.deployErr
	}
	f.deployed = true
	return &compose.DeployResult{}, nil
}
func (f *fakeHost) StackStatus(context.Context, string) ([]compose.StatusEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.deployed {
		return nil, nil
	}
	return []compose.StatusEntry{{Service: "web", State: "running"}}, nil
}

type fakeHosts map[string]*fakeHost

func (h fakeHosts) Pick(id string) (host.Host, error) {
	if f, ok := h[id]; ok {
		return f, nil
	}
	return nil, host.ErrUnknownHost
}
func (h fakeHosts) List(context.Context) ([]host.Info, error) { return nil, nil }

type migrationEnv struct {
	svc            *Service
	mgr            *stacks.Manager
	source, target *fakeHost
}

func newMigrationEnv(t *testing.T) *migrationEnv {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	mgr.SetSumStore(stacks.NewSumStore(database))
	if _, err := mgr.Create("web", "services:\n  web:\n    image: nginx\n", "A=1\n"); err != nil {
		t.Fatal(err)
	}
	dir, err := mgr.Dir("web")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.prod.yaml"), []byte("services:\n  web:\n    image: nginx:prod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scaling.SaveMeta(dir, &scaling.MetaFile{ActiveEnvironment: "prod"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.AcceptExternal(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordOverride("web", "prod"); err != nil {
		t.Fatal(err)
	}
	deps := stacks.NewDeploymentStore(database)
	if err := deps.Set(context.Background(), "web", "local", "deployed"); err != nil {
		t.Fatal(err)
	}
	e := &migrationEnv{
		mgr:    mgr,
		source: &fakeHost{id: "local", deployed: true},
		target: &fakeHost{id: "agent1"},
	}
	e.svc = NewService(database, fakeHosts{"local": e.source, "agent1": e.target}, mgr, deps)
	e.svc.SetGlobalEnv(func(context.Context) ([]globalenv.Var, error) {
		return []globalenv.Var{{Key: "GLOBAL", Value: "g"}}, nil
	})
	return e
}

// migrate runs one migration to the end and returns its final record.
func (e *migrationEnv) migrate(t *testing.T) *Migration {
	t.Helper()
	m, err := e.svc.Initiate(context.Background(), "web", "agent1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		// Check isActive before reading the record: the run stores its final
		// status before leaving the active set, so a record read first could
		// be a failure a rollback is about to replace.
		running := e.svc.isActive(m.ID)
		got, err := e.svc.Store().Get(context.Background(), m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !running && got.IsTerminal() {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("migration did not finish")
	return nil
}

func (s *Service) isActive(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[id]
	return ok
}

func TestMigrationRollback(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name          string
		setup         func(e *migrationEnv)
		wantStatus    string
		sourceDeploys int
		targetStops   int
		sourceUp      bool
	}{
		{
			name:          "source stop fails",
			setup:         func(e *migrationEnv) { e.source.stopErr = boom },
			wantStatus:    StatusRolledBack,
			sourceDeploys: 1,
			targetStops:   0,
			sourceUp:      true,
		},
		{
			name:          "target deploy fails",
			setup:         func(e *migrationEnv) { e.target.deployErr = boom },
			wantStatus:    StatusRolledBack,
			sourceDeploys: 1,
			targetStops:   1,
			sourceUp:      true,
		},
		{
			name:          "completes",
			setup:         func(*migrationEnv) {},
			wantStatus:    StatusCompleted,
			sourceDeploys: 0,
			targetStops:   0,
			sourceUp:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newMigrationEnv(t)
			tt.setup(e)
			m := e.migrate(t)
			if m.Status != tt.wantStatus {
				t.Fatalf("status = %s (%s), want %s", m.Status, m.ErrorMessage, tt.wantStatus)
			}
			if len(e.source.deploys) != tt.sourceDeploys {
				t.Errorf("source deploys = %d, want %d", len(e.source.deploys), tt.sourceDeploys)
			}
			if e.target.stops != tt.targetStops {
				t.Errorf("target stops = %d, want %d", e.target.stops, tt.targetStops)
			}
			if e.source.deployed != tt.sourceUp {
				t.Errorf("source running = %v, want %v", e.source.deployed, tt.sourceUp)
			}
			for _, d := range append(e.source.deploys, e.target.deploys...) {
				if !strings.Contains(d, "nginx:prod") || !strings.Contains(d, "GLOBAL=g") || !strings.Contains(d, "A=1") {
					t.Errorf("deploy without the override or global env:\n%s", d)
				}
			}
		})
	}
}

func TestMigrationRefusesFilesChangedOutside(t *testing.T) {
	e := newMigrationEnv(t)
	dir, err := e.mgr.Dir("web")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("A=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := e.migrate(t)
	if m.Status != StatusFailed || !strings.Contains(m.ErrorMessage, ".env") {
		t.Fatalf("status = %s (%s), want failed for .env", m.Status, m.ErrorMessage)
	}
	if e.source.stops != 0 || len(e.target.deploys) != 0 || len(e.source.deploys) != 0 {
		t.Errorf("stack touched: source stops %d, target deploys %d, source deploys %d",
			e.source.stops, len(e.target.deploys), len(e.source.deploys))
	}
}

// Override files are not written by Dockmesh, so a migration only uses the
// override a manual deploy confirmed.
func TestMigrationRefusesUnconfirmedOverride(t *testing.T) {
	e := newMigrationEnv(t)
	dir, err := e.mgr.Dir("web")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.prod.yaml"), []byte("services:\n  web:\n    image: evil\n    privileged: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Confirmed as a file, so only the override check stands in the way.
	if err := e.mgr.AcceptExternal(context.Background(), "web"); err != nil {
		t.Fatal(err)
	}
	m := e.migrate(t)
	if m.Status != StatusFailed || !strings.Contains(m.ErrorMessage, "override") {
		t.Fatalf("status = %s (%s), want failed for the override", m.Status, m.ErrorMessage)
	}
	if e.source.stops != 0 || len(e.target.deploys) != 0 {
		t.Errorf("stack touched: source stops %d, target deploys %d", e.source.stops, len(e.target.deploys))
	}
}

// busyStore is a Store on a connection that fails at once when another
// connection holds the write lock.
func busyStore(t *testing.T) (*Store, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO migrations (id, stack_name, source_host_id, target_host_id, status, phase, initiated_by)
		VALUES ('m1', 'web', 'local', 'agent1', 'failed', 'rollback', 'u1')`); err != nil {
		t.Fatal(err)
	}
	impatient, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { impatient.Close() })
	return NewStore(impatient), database, "m1"
}

func TestRolledBackStatusSurvivesBusyDatabase(t *testing.T) {
	store, database, id := busyStore(t)
	saved := statusRetryDelays
	statusRetryDelays = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, time.Second}
	t.Cleanup(func() { statusRetryDelays = saved })
	ctx := context.Background()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE migrations SET phase = phase WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateStatus(ctx, id, StatusRolledBack, "rollback", "done"); !isBusy(err) {
		t.Fatalf("precondition: a plain write while locked = %v, want SQLITE_BUSY", err)
	}
	release := time.AfterFunc(200*time.Millisecond, func() { _ = tx.Commit() })
	defer release.Stop()

	if err := store.updateStatusRetry(ctx, id, StatusRolledBack, "rollback", "done"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	m, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != StatusRolledBack {
		t.Fatalf("status = %s, want %s", m.Status, StatusRolledBack)
	}
}

func TestUpdateStatusRetryStopsOnOtherErrors(t *testing.T) {
	store, _, id := busyStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := store.updateStatusRetry(ctx, id, StatusRolledBack, "rollback", "done"); err == nil {
		t.Fatal("no error on a cancelled context")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("kept retrying an error that isn't SQLITE_BUSY")
	}
}

func TestIsBusy(t *testing.T) {
	if isBusy(nil) || isBusy(errors.New("database is locked")) {
		t.Fatal("isBusy matched something that is not a SQLite busy error")
	}
}

func TestMigrationRefusesMetaAndOverrideChangedOutside(t *testing.T) {
	for _, file := range []string{".dockmesh.meta.json", "compose.prod.yaml", "compose.new.yaml"} {
		t.Run(file, func(t *testing.T) {
			e := newMigrationEnv(t)
			dir, err := e.mgr.Dir("web")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, file), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			m := e.migrate(t)
			if m.Status != StatusFailed || !strings.Contains(m.ErrorMessage, "changed outside") || !strings.Contains(m.ErrorMessage, file) {
				t.Fatalf("status = %s (%s), want failed for %s", m.Status, m.ErrorMessage, file)
			}
			if e.source.stops != 0 || len(e.target.deploys) != 0 {
				t.Errorf("stack touched: source stops %d, target deploys %d", e.source.stops, len(e.target.deploys))
			}
		})
	}
}
