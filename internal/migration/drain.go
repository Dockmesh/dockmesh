package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
// migrations sequentially in plan order, pausing on failure.
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

// drainPoll is how often await polls a running migration.
var drainPoll = 3 * time.Second

// Execute runs the drain plan sequentially. Blocks until all stacks
// are migrated, the drain is aborted, or the context is cancelled.
func (o *DrainOrchestrator) Execute(ctx context.Context, drain *Drain, userID string, confirmHooks map[string]string) {
	slog.Info("drain executing",
		"id", drain.ID, "host", drain.SourceHostID,
		"stacks", len(drain.Plan))

	_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainExecuting)

	for _, entry := range drain.Plan {
		if !o.wait(ctx, drain.ID) {
			o.stopped(ctx, drain.ID)
			return
		}
		if !entry.Feasible {
			slog.Warn("drain: skipping infeasible entry",
				"stack", entry.StackName, "detail", entry.Detail)
			continue
		}

		// The caller's rights were checked for the stack on this host.
		m, err := o.migSvc.initiate(ctx, entry.StackName, drain.SourceHostID, entry.TargetHostID, userID, confirmHooks[entry.StackName], nil)
		if err != nil {
			slog.Warn("drain: migration initiate failed — pausing",
				"stack", entry.StackName, "err", err)
		} else if status := o.await(ctx, m.ID); status != StatusCompleted {
			slog.Warn("drain: migration did not complete — pausing",
				"stack", entry.StackName, "status", status)
		} else {
			continue
		}
		o.Pause()
		if !o.wait(ctx, drain.ID) {
			o.stopped(ctx, drain.ID)
			return
		}
	}

	_ = o.drainStore.UpdateStatus(ctx, drain.ID, DrainCompleted)
	slog.Info("drain completed", "id", drain.ID)
}

// await polls migration id until it is terminal and returns its status,
// "" when it can't be read, ctx ends or the drain is aborted. An aborted
// drain leaves the migration to finish or roll back on its own.
func (o *DrainOrchestrator) await(ctx context.Context, id string) string {
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(drainPoll):
		}
		o.mu.Lock()
		aborted := o.aborted
		o.mu.Unlock()
		if aborted {
			return ""
		}
		cur, err := o.migSvc.Store().Get(ctx, id)
		if err != nil {
			return ""
		}
		if cur.IsTerminal() {
			return cur.Status
		}
	}
}

// wait blocks while the drain is paused. It reports false once the drain
// is aborted or ctx ends.
func (o *DrainOrchestrator) wait(ctx context.Context, drainID string) bool {
	o.mu.Lock()
	if o.aborted || !o.paused {
		aborted := o.aborted
		o.mu.Unlock()
		return !aborted && ctx.Err() == nil
	}
	resumed := o.pauseCh
	o.mu.Unlock()

	_ = o.drainStore.UpdateStatus(ctx, drainID, DrainPaused)
	slog.Info("drain paused", "id", drainID)
	select {
	case <-resumed:
	case <-ctx.Done():
		return false
	}
	o.mu.Lock()
	aborted := o.aborted
	o.mu.Unlock()
	if aborted {
		return false
	}
	_ = o.drainStore.UpdateStatus(ctx, drainID, DrainExecuting)
	return true
}

// stopped records an aborted drain; a cancelled ctx leaves the record as
// it is.
func (o *DrainOrchestrator) stopped(ctx context.Context, drainID string) {
	if ctx.Err() != nil {
		return
	}
	_ = o.drainStore.UpdateStatus(ctx, drainID, DrainAborted)
	slog.Info("drain aborted", "id", drainID)
}

func (o *DrainOrchestrator) Pause() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.paused {
		o.paused = true
		o.pauseCh = make(chan struct{})
	}
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
		close(o.pauseCh)
	}
}

// ErrDrainServerHost refuses a drain of the host Dockmesh itself runs on.
var ErrDrainServerHost = errors.New("the local server runs Dockmesh itself and cannot be drained")

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

// Plan assigns the host's stacks to targets and lists each stack's hook
// commands, which the drain would run.
func (s *DrainService) Plan(ctx context.Context, sourceHostID string) (*DrainPlan, error) {
	if sourceHostID == "" || sourceHostID == "local" {
		return nil, ErrDrainServerHost
	}
	plan, err := PlanDrain(ctx, sourceHostID, s.migSvc.hosts, s.migSvc.deployments, s.migSvc.stacks)
	if err != nil {
		return nil, err
	}
	for i := range plan.Entries {
		e := &plan.Entries[i]
		vols, err := s.migSvc.namedVolumes(ctx, e.StackName)
		if err == nil && len(vols) > 0 {
			err = fmt.Errorf("%w: %s", ErrNamedVolumes, strings.Join(vols, ", "))
		}
		if err != nil {
			e.TargetHostID, e.TargetName = "", ""
			e.Feasible, e.Detail = false, err.Error()
			plan.Feasible = false
			continue
		}
		hooks, err := s.migSvc.HookCommands(e.StackName)
		if err != nil {
			e.Feasible, e.Detail = false, err.Error()
			plan.Feasible = false
			continue
		}
		e.Hooks = hooks
	}
	return plan, nil
}

// DrainBlockedError refuses a drain with stacks that can't move, listed in
// the plan with the reason.
type DrainBlockedError struct {
	Plan *DrainPlan
}

func (e *DrainBlockedError) Error() string {
	var names []string
	for _, en := range e.Plan.Entries {
		if !en.Feasible {
			names = append(names, en.StackName)
		}
	}
	return "these stacks can't move to another host: " + strings.Join(names, ", ")
}

// DrainHooksUnconfirmedError refuses a drain whose plan runs hook
// commands that confirmHooks doesn't name by their digest.
type DrainHooksUnconfirmedError struct {
	Entries []PlanEntry
}

func (e *DrainHooksUnconfirmedError) Error() string {
	return fmt.Sprintf("%d stack(s): %s", len(e.Entries), ErrHooksUnconfirmed)
}

func (e *DrainHooksUnconfirmedError) Is(target error) bool { return target == ErrHooksUnconfirmed }

// Execute plans the drain again and runs it. authorize, when set, vets
// that plan, the one that runs, before anything else. confirmHooks maps a
// stack name to the digest of the hook commands the caller was shown;
// every stack in the plan with hooks needs its current digest there.
func (s *DrainService) Execute(ctx context.Context, sourceHostID, userID string, confirmHooks map[string]string, authorize func([]PlanEntry) error) (*Drain, error) {
	plan, err := s.Plan(ctx, sourceHostID)
	if err != nil {
		return nil, err
	}
	if authorize != nil {
		if err := authorize(plan.Entries); err != nil {
			return nil, err
		}
	}
	if !plan.Feasible {
		return nil, &DrainBlockedError{Plan: plan}
	}
	var unconfirmed []PlanEntry
	for _, e := range plan.Entries {
		if e.Feasible && e.Hooks != nil && confirmHooks[e.StackName] != e.Hooks.Digest {
			unconfirmed = append(unconfirmed, e)
		}
	}
	if len(unconfirmed) > 0 {
		return nil, &DrainHooksUnconfirmedError{Entries: unconfirmed}
	}

	// The drain record keeps only the digests: the commands can hold
	// secrets and would outlive a change of the meta file in the DB.
	stored := make([]PlanEntry, len(plan.Entries))
	for i, e := range plan.Entries {
		if e.Hooks != nil {
			e.Hooks = &HookCommands{Digest: e.Hooks.Digest}
		}
		stored[i] = e
	}
	now := time.Now()
	d := &Drain{
		ID:           uuid.NewString(),
		SourceHostID: sourceHostID,
		Status:       DrainPlanning,
		Plan:         stored,
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
		orch.Execute(context.Background(), d, userID, confirmHooks)
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
