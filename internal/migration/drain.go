package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DrainStatus constants.
const (
	DrainPlanning  = "planning"
	DrainExecuting = "executing"
	DrainPaused    = "paused"
	DrainCompleted = "completed"
	DrainAborted   = "aborted"
)

// Drain is one row of the drains table.
type Drain struct {
	ID           string      `json:"id"`
	SourceHostID string      `json:"source_host_id"`
	Status       string      `json:"status"`
	Plan         []PlanEntry `json:"plan"`
	StartedAt    *time.Time  `json:"started_at,omitempty"`
	CompletedAt  *time.Time  `json:"completed_at,omitempty"`
	InitiatedBy  string      `json:"initiated_by"`
	CreatedAt    time.Time   `json:"created_at"`
	// Runtime: per-stack migration status, not persisted in DB but
	// populated from the migrations table on read.
	StackStatus []DrainStackStatus `json:"stack_status,omitempty"`
}

// DrainStackStatus shows the per-stack migration state within a drain.
type DrainStackStatus struct {
	StackName   string `json:"stack_name"`
	MigrationID string `json:"migration_id,omitempty"`
	Status      string `json:"status"` // pending | migrating | completed | failed | skipped
}

// DrainStore provides CRUD for the drains table.
type DrainStore struct {
	db *sql.DB
}

func NewDrainStore(db *sql.DB) *DrainStore { return &DrainStore{db: db} }

func (s *DrainStore) Create(ctx context.Context, d *Drain) error {
	planJSON, _ := json.Marshal(d.Plan)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drains (id, source_host_id, status, plan_json, started_at, initiated_by)
		VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, d.SourceHostID, d.Status, string(planJSON), d.StartedAt, d.InitiatedBy)
	return err
}

func (s *DrainStore) UpdateStatus(ctx context.Context, id, status string) error {
	var completedAt *time.Time
	if status == DrainCompleted || status == DrainAborted {
		now := time.Now()
		completedAt = &now
	}
	_, err := s.db.ExecContext(ctx, `UPDATE drains SET status = ?, completed_at = ? WHERE id = ?`,
		status, completedAt, id)
	return err
}

func (s *DrainStore) Get(ctx context.Context, id string) (*Drain, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, source_host_id, status, plan_json, started_at, completed_at, initiated_by, created_at
		FROM drains WHERE id = ?`, id)
	return scanDrain(row)
}

func (s *DrainStore) ListByHost(ctx context.Context, hostID string) ([]*Drain, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_host_id, status, plan_json, started_at, completed_at, initiated_by, created_at
		FROM drains WHERE source_host_id = ? ORDER BY created_at DESC`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Drain
	for rows.Next() {
		d, err := scanDrain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type drainScanner interface{ Scan(dest ...any) error }

func scanDrain(r drainScanner) (*Drain, error) {
	var d Drain
	var planJSON string
	var started, completed sql.NullTime
	if err := r.Scan(&d.ID, &d.SourceHostID, &d.Status, &planJSON,
		&started, &completed, &d.InitiatedBy, &d.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if started.Valid {
		d.StartedAt = &started.Time
	}
	if completed.Valid {
		d.CompletedAt = &completed.Time
	}
	_ = json.Unmarshal([]byte(planJSON), &d.Plan)
	return &d, nil
}

// DrainOrchestrator manages an in-flight drain operation. It runs
// P.9 migrations sequentially in plan order, pausing on failure.
type DrainOrchestrator struct {
	migSvc     *Service
	drainStore *DrainStore

	mu      sync.Mutex
	paused  bool
	pauseCh chan struct{} // closed on resume
	aborted bool
}

func NewDrainOrchestrator(migSvc *Service, ds *DrainStore) *DrainOrchestrator {
	return &DrainOrchestrator{
		migSvc:     migSvc,
		drainStore: ds,
		pauseCh:    make(chan struct{}),
	}
}

// Execute runs the drain plan sequentially. Blocks until all stacks
// are migrated, the drain is aborted, or the context is cancelled.
func (o *DrainOrchestrator) Execute(ctx context.Context, drain *Drain, userID string) {
	slog.Info("drain executing",
		"id", drain.ID, "host", drain.SourceHostID,
		"stacks", len(drain.Plan))

	_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainExecuting)

	for _, entry := range drain.Plan {
		// Check abort.
		o.mu.Lock()
		if o.aborted {
			o.mu.Unlock()
			_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainAborted)
			slog.Info("drain aborted", "id", drain.ID)
			return
		}
		// Check pause.
		if o.paused {
			o.mu.Unlock()
			_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainPaused)
			slog.Info("drain paused", "id", drain.ID)
			select {
			case <-o.pauseCh:
				_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainExecuting)
			case <-ctx.Done():
				return
			}
		} else {
			o.mu.Unlock()
		}

		if !entry.Feasible {
			slog.Warn("drain: skipping infeasible entry",
				"stack", entry.StackName, "detail", entry.Detail)
			continue
		}

		// Initiate migration for this stack.
		m, err := o.migSvc.Initiate(ctx, entry.StackName, entry.TargetHostID, userID)
		if err != nil {
			slog.Warn("drain: migration initiate failed",
				"stack", entry.StackName, "err", err)
			// Pause for user decision.
			o.mu.Lock()
			o.paused = true
			o.pauseCh = make(chan struct{})
			o.mu.Unlock()
			_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainPaused)
			select {
			case <-o.pauseCh:
				_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainExecuting)
			case <-ctx.Done():
				return
			}
			continue
		}

		// Wait for migration to complete.
		for {
			time.Sleep(3 * time.Second)
			cur, err := o.migSvc.Store().Get(ctx, m.ID)
			if err != nil {
				break
			}
			if cur.IsTerminal() {
				if cur.Status == StatusFailed || cur.Status == StatusRolledBack {
					slog.Warn("drain: migration failed — pausing",
						"stack", entry.StackName, "status", cur.Status)
					o.mu.Lock()
					o.paused = true
					o.pauseCh = make(chan struct{})
					o.mu.Unlock()
					_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainPaused)
					select {
					case <-o.pauseCh:
						_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainExecuting)
					case <-ctx.Done():
						return
					}
				}
				break
			}
		}
	}

	_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainCompleted)
	slog.Info("drain completed", "id", drain.ID)
}

func (o *DrainOrchestrator) Pause() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.paused = true
	o.pauseCh = make(chan struct{})
}

func (o *DrainOrchestrator) Resume() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.paused {
		o.paused = false
		close(o.pauseCh)
	}
}

func (o *DrainOrchestrator) Abort() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.aborted = true
	if o.paused {
		o.paused = false
		close(o.pauseCh) // unblock
	}
}

// DrainService wraps the orchestrator lifecycle for the API.
type DrainService struct {
	migSvc     *Service
	drainStore *DrainStore

	mu     sync.Mutex
	active map[string]*DrainOrchestrator // drain ID → orchestrator
}

func NewDrainService(migSvc *Service, db *sql.DB) *DrainService {
	return &DrainService{
		migSvc:     migSvc,
		drainStore: NewDrainStore(db),
		active:     make(map[string]*DrainOrchestrator),
	}
}

func (s *DrainService) Store() *DrainStore { return s.drainStore }

func (s *DrainService) Plan(ctx context.Context, sourceHostID string) (*DrainPlan, error) {
	return PlanDrain(ctx, sourceHostID, s.migSvc.hosts, s.migSvc.deployments, s.migSvc.stacks)
}

func (s *DrainService) Execute(ctx context.Context, sourceHostID, userID string) (*Drain, error) {
	plan, err := s.Plan(ctx, sourceHostID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	d := &Drain{
		ID:           uuid.NewString(),
		SourceHostID: sourceHostID,
		Status:       DrainPlanning,
		Plan:         plan.Entries,
		StartedAt:    &now,
		InitiatedBy:  userID,
	}
	if err := s.drainStore.Create(ctx, d); err != nil {
		return nil, err
	}

	orch := NewDrainOrchestrator(s.migSvc, s.drainStore)
	s.mu.Lock()
	s.active[d.ID] = orch
	s.mu.Unlock()

	go func() {
		orch.Execute(context.Background(), d, userID)
		s.mu.Lock()
		delete(s.active, d.ID)
		s.mu.Unlock()
	}()

	return d, nil
}

func (s *DrainService) Get(ctx context.Context, drainID string) (*Drain, error) {
	return s.drainStore.Get(ctx, drainID)
}

func (s *DrainService) PauseDrain(drainID string) error {
	s.mu.Lock()
	orch, ok := s.active[drainID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("drain not active")
	}
	orch.Pause()
	return nil
}

func (s *DrainService) ResumeDrain(drainID string) error {
	s.mu.Lock()
	orch, ok := s.active[drainID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("drain not active")
	}
	orch.Resume()
	return nil
}

func (s *DrainService) AbortDrain(drainID string) error {
	s.mu.Lock()
	orch, ok := s.active[drainID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("drain not active")
	}
	orch.Abort()
	return nil
}
