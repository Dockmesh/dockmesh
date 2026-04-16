package migration

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/google/uuid"
)

// Service orchestrates stack migrations. Each migration runs as a
// goroutine that advances through the 10-phase state machine. Only
// one migration per stack is allowed at a time.
type Service struct {
	store       *Store
	hosts       *host.Registry
	stacks      *stacks.Manager
	deployments *stacks.DeploymentStore
	db          *sql.DB

	mu     sync.Mutex
	active map[string]context.CancelFunc // migration ID → cancel
}

func NewService(db *sql.DB, hr *host.Registry, sm *stacks.Manager, ds *stacks.DeploymentStore) *Service {
	return &Service{
		store:       NewStore(db),
		hosts:       hr,
		stacks:      sm,
		deployments: ds,
		db:          db,
		active:      make(map[string]context.CancelFunc),
	}
}

// Store exposes the underlying store for handler queries.
func (s *Service) Store() *Store { return s.store }

// Start marks any interrupted migrations as failed (server restart
// during active migration → Decision Q9-2: no auto-resume).
func (s *Service) Start(ctx context.Context) error {
	active, err := s.store.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, m := range active {
		slog.Warn("migration interrupted by server restart — marking failed",
			"id", m.ID, "stack", m.StackName, "status", m.Status)
		_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, m.Phase,
			"server restarted during migration")
	}
	return nil
}

// Initiate starts a new migration. Returns the migration record.
// The actual work happens asynchronously in a goroutine.
func (s *Service) Initiate(ctx context.Context, stackName, targetHostID, userID string) (*Migration, error) {
	// Validate stack exists.
	if _, err := s.stacks.Get(stackName); err != nil {
		return nil, fmt.Errorf("stack %q: %w", stackName, err)
	}

	// Look up source host from deployment table.
	dep, err := s.deployments.Get(ctx, stackName)
	if err != nil {
		return nil, err
	}
	if dep == nil {
		return nil, fmt.Errorf("stack %q has no deployment — deploy it first", stackName)
	}
	sourceHostID := dep.HostID
	if sourceHostID == targetHostID {
		return nil, fmt.Errorf("source and target are the same host")
	}

	// Single-concurrency per stack.
	hasActive, err := s.store.HasActive(ctx, stackName)
	if err != nil {
		return nil, err
	}
	if hasActive {
		return nil, fmt.Errorf("stack %q already has an active migration", stackName)
	}

	now := time.Now()
	m := &Migration{
		ID:           uuid.NewString(),
		StackName:    stackName,
		SourceHostID: sourceHostID,
		TargetHostID: targetHostID,
		Status:       StatusPending,
		StartedAt:    &now,
		InitiatedBy:  userID,
	}
	if err := s.store.Create(ctx, m); err != nil {
		return nil, err
	}

	// Launch the migration goroutine.
	mctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.active[m.ID] = cancel
	s.mu.Unlock()
	go s.run(mctx, m.ID)

	return m, nil
}

// run is the migration goroutine. It advances through the 10 phases.
func (s *Service) run(ctx context.Context, migrationID string) {
	defer func() {
		s.mu.Lock()
		delete(s.active, migrationID)
		s.mu.Unlock()
	}()

	m, err := s.store.Get(ctx, migrationID)
	if err != nil {
		slog.Error("migration load failed", "id", migrationID, "err", err)
		return
	}

	phases := []struct {
		status string
		fn     func(context.Context, *Migration) error
	}{
		{StatusPreflight, s.phasePreflight},
		{StatusPreparing, s.phasePrepare},
		{StatusPreDump, s.phasePreDump},
		{StatusStopping, s.phaseStopping},
		{StatusSyncing, s.phaseSyncing},
		{StatusStarting, s.phaseStarting},
		{StatusPostRestore, s.phasePostRestore},
		{StatusHealthCheck, s.phaseHealthCheck},
		{StatusCommitting, s.phaseCommit},
	}

	for _, p := range phases {
		if ctx.Err() != nil {
			_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, p.status, "cancelled")
			return
		}
		_ = s.store.UpdateStatus(ctx, m.ID, p.status, p.status, "")
		m.Status = p.status
		m.Phase = p.status

		if err := p.fn(ctx, m); err != nil {
			slog.Warn("migration phase failed",
				"id", m.ID, "stack", m.StackName,
				"phase", p.status, "err", err)
			_ = s.store.UpdateStatus(ctx, m.ID, StatusFailed, p.status, err.Error())

			// Auto-rollback if we were past the stopping phase.
			if p.status == StatusStarting || p.status == StatusPostRestore || p.status == StatusHealthCheck {
				s.rollback(context.Background(), m)
			}
			return
		}
	}

	_ = s.store.UpdateStatus(ctx, m.ID, StatusCompleted, "done", "")
	slog.Info("migration completed", "id", m.ID, "stack", m.StackName)
}

// Phase stubs — each will be implemented in subsequent commits.

func (s *Service) phasePreflight(ctx context.Context, m *Migration) error {
	slog.Info("migration preflight", "id", m.ID, "stack", m.StackName)
	// TODO: validate target online, capacity, images, arch
	return nil
}

func (s *Service) phasePrepare(ctx context.Context, m *Migration) error {
	slog.Info("migration prepare", "id", m.ID)
	// TODO: sync compose files to target, pull images
	return nil
}

func (s *Service) phasePreDump(ctx context.Context, m *Migration) error {
	slog.Info("migration pre-dump", "id", m.ID)
	// TODO: execute pre_dump hooks if configured
	return nil
}

func (s *Service) phaseStopping(ctx context.Context, m *Migration) error {
	slog.Info("migration stopping source", "id", m.ID)
	source, err := s.hosts.Pick(m.SourceHostID)
	if err != nil {
		return fmt.Errorf("source host: %w", err)
	}
	return source.StopStack(ctx, m.StackName)
}

func (s *Service) phaseSyncing(ctx context.Context, m *Migration) error {
	slog.Info("migration syncing volumes", "id", m.ID)
	// TODO: tar-stream volumes from source to target
	return nil
}

func (s *Service) phaseStarting(ctx context.Context, m *Migration) error {
	slog.Info("migration starting on target", "id", m.ID)
	target, err := s.hosts.Pick(m.TargetHostID)
	if err != nil {
		return fmt.Errorf("target host: %w", err)
	}
	detail, err := s.stacks.Get(m.StackName)
	if err != nil {
		return err
	}
	_, err = target.DeployStack(ctx, m.StackName, detail.Compose, detail.Env)
	return err
}

func (s *Service) phasePostRestore(ctx context.Context, m *Migration) error {
	slog.Info("migration post-restore", "id", m.ID)
	// TODO: execute post_restore hooks if configured
	return nil
}

func (s *Service) phaseHealthCheck(ctx context.Context, m *Migration) error {
	slog.Info("migration health-check", "id", m.ID)
	// Poll for 5 minutes: all containers must be running.
	target, err := s.hosts.Pick(m.TargetHostID)
	if err != nil {
		return fmt.Errorf("target host: %w", err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		status, err := target.StackStatus(ctx, m.StackName)
		if err == nil && len(status) > 0 {
			allRunning := true
			for _, entry := range status {
				if entry.State != "running" {
					allRunning = false
					break
				}
			}
			if allRunning {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("containers did not reach running within 5 minutes")
}

func (s *Service) phaseCommit(ctx context.Context, m *Migration) error {
	slog.Info("migration commit", "id", m.ID)
	// Update deployment to point at the new host.
	if err := s.deployments.Set(ctx, m.StackName, m.TargetHostID, "deployed"); err != nil {
		return err
	}
	// Mark source as migrated_away.
	_ = s.deployments.Set(ctx, m.StackName+"__source", m.SourceHostID, "migrated_away")
	return nil
}

// rollback restarts the source stack after a failed migration.
func (s *Service) rollback(ctx context.Context, m *Migration) {
	slog.Warn("migration rollback", "id", m.ID, "stack", m.StackName)
	source, err := s.hosts.Pick(m.SourceHostID)
	if err != nil {
		slog.Error("rollback: source host unavailable", "err", err)
		return
	}
	detail, err := s.stacks.Get(m.StackName)
	if err != nil {
		slog.Error("rollback: stack get failed", "err", err)
		return
	}
	if _, err := source.DeployStack(ctx, m.StackName, detail.Compose, detail.Env); err != nil {
		slog.Error("rollback: restart source failed", "err", err)
	}
	_ = s.store.UpdateStatus(ctx, m.ID, StatusRolledBack, "rollback", "auto-rollback after failure")
}

// Rollback triggers a manual rollback for a completed migration.
func (s *Service) Rollback(ctx context.Context, migrationID string) error {
	m, err := s.store.Get(ctx, migrationID)
	if err != nil {
		return err
	}
	if m.Status != StatusCompleted {
		return fmt.Errorf("can only rollback completed migrations (current: %s)", m.Status)
	}
	go s.rollback(context.Background(), m)
	return nil
}
