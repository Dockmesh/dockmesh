// Package operations records long-running work (image updates, backup
// runs, restores, new proxy sites) as an operation with ordered steps and
// a capped log, so the UI can follow it live and show it after it ended.
//
// The code doing the work gets an *Op from the context and reports the
// steps it really performs; every method is a no-op on a nil *Op, so the
// same code runs unchanged where nobody tracks it.
package operations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Kinds of operation.
const (
	KindImageUpdate   = "image.update"
	KindBackupRun     = "backup.run"
	KindBackupRestore = "backup.restore"
	KindProxySite     = "proxy.site"
)

// BackupRunPrefix starts the ref of operations on a backup run (the run
// and its restores).
const BackupRunPrefix = "backup-run:"

func BackupRunRef(id int64) string { return BackupRunPrefix + strconv.FormatInt(id, 10) }

// HumanBytes formats n with SI units for step details.
func HumanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Active reports whether the operation has not ended yet.
func (s Status) Active() bool { return s == StatusQueued || s == StatusRunning }

type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
	// StepWarning ends a step whose outcome could not be confirmed
	// without failing the operation, e.g. a certificate not seen yet.
	StepWarning StepStatus = "warning"
	// StepCancelled ends the step that ran when a user cancelled.
	StepCancelled StepStatus = "cancelled"
)

const (
	MaxLogLines  = 500
	MaxLineBytes = 1024
	Retention    = 7 * 24 * time.Hour

	// RestartReason is the error of operations that were still running
	// when the server stopped.
	RestartReason = "the server restarted while this was running"
)

type Step struct {
	Key    string     `json:"key"`
	Label  string     `json:"label"`
	Status StepStatus `json:"status"`
	Detail string     `json:"detail,omitempty"`
	// Progress is 0..1, or nil when the step cannot measure its progress.
	Progress   *float64   `json:"progress,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type LogLine struct {
	TS    time.Time `json:"ts"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

type Operation struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Ref     string `json:"ref,omitempty"`
	GroupID string `json:"group_id,omitempty"`
	HostID  string `json:"host_id,omitempty"`
	Stack   string `json:"stack,omitempty"`
	// AdminOnly marks work on the whole install (system backups); only
	// unscoped admins see it.
	AdminOnly bool   `json:"-"`
	Actor     string `json:"actor,omitempty"`
	Status    Status `json:"status"`
	// Cancellable is set while a cancel request would stop the work;
	// CancelRequested once one was made and the work is winding down.
	Cancellable     bool       `json:"cancellable,omitempty"`
	CancelRequested bool       `json:"cancel_requested,omitempty"`
	Error           string     `json:"error,omitempty"`
	Steps           []Step     `json:"steps"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	Log             []LogLine  `json:"log,omitempty"`
}

// Spec describes an operation to start. Steps are the phases the caller
// will report, in order. Secrets are values masked in every log line,
// step detail and error of the operation.
type Spec struct {
	Kind      string
	Target    string
	Ref       string
	GroupID   string
	HostID    string
	Stack     string
	AdminOnly bool
	Actor     string
	Steps     []StepSpec
	// Queued starts the operation as queued; Begin moves it to running.
	Queued  bool
	Secrets []string
}

type StepSpec struct {
	Key   string
	Label string
}

// Event is published for every change of a live operation. Op never
// carries the log; Line is set for "log" events.
type Event struct {
	Type string    `json:"type"`
	Op   Operation `json:"op"`
	Line *LogLine  `json:"line,omitempty"`
}

var ErrNotFound = errors.New("operation not found")

// ErrNotCancellable is returned for operations that are not queued or
// whose work cannot be stopped safely.
var ErrNotCancellable = errors.New("operation cannot be cancelled")

// ErrCancelled is the cause of the context of work a user cancelled.
var ErrCancelled = errors.New("cancelled by a user")

type Service struct {
	db  *sql.DB
	now func() time.Time

	mu      sync.Mutex
	live    map[string]*Op
	subs    map[int]chan Event
	nextSub int
}

func New(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now, live: map[string]*Op{}, subs: map[int]chan Event{}}
}

// Start records a new operation and returns its handle. It never fails:
// when the row cannot be written the work still runs and is tracked in
// memory only.
func (s *Service) Start(ctx context.Context, spec Spec) *Op {
	if s == nil {
		return nil
	}
	now := s.now().UTC()
	o := &Op{s: s, ctx: context.WithoutCancel(ctx)}
	o.addSecrets(spec.Secrets)
	op := Operation{
		ID:        newID(),
		Kind:      spec.Kind,
		Target:    truncate(o.mask(spec.Target)),
		Ref:       spec.Ref,
		GroupID:   spec.GroupID,
		HostID:    spec.HostID,
		Stack:     spec.Stack,
		AdminOnly: spec.AdminOnly,
		Actor:     spec.Actor,
		Status:    StatusRunning,
		Steps:     make([]Step, 0, len(spec.Steps)),
		CreatedAt: now,
	}
	if spec.Queued {
		op.Status = StatusQueued
	} else {
		op.StartedAt = &now
	}
	for _, st := range spec.Steps {
		op.Steps = append(op.Steps, Step{Key: st.Key, Label: st.Label, Status: StepPending})
	}
	o.op = op
	if err := s.insert(context.WithoutCancel(ctx), &op); err != nil {
		slog.Warn("operations: record start", "kind", op.Kind, "err", err)
	}
	s.mu.Lock()
	s.live[op.ID] = o
	s.mu.Unlock()
	s.publish(Event{Type: "update", Op: o.snapshot(false)})
	return o
}

// Get returns one operation with its log, live state first.
func (s *Service) Get(ctx context.Context, id string) (*Operation, error) {
	s.mu.Lock()
	o := s.live[id]
	s.mu.Unlock()
	if o != nil {
		op := o.snapshot(true)
		return &op, nil
	}
	row := s.db.QueryRowContext(ctx, selectOne+` WHERE id = ?`, id)
	op, err := scanOp(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return op, err
}

// Filter narrows List. Zero values match everything.
type Filter struct {
	Kind       string // exact kind, or a prefix ending in "."
	Ref        string
	GroupID    string
	ActiveOnly bool
	Limit      int
}

// List returns operations newest first, without their logs.
func (s *Service) List(ctx context.Context, f Filter) ([]Operation, error) {
	q := selectList + ` WHERE 1=1`
	var args []any
	switch {
	case strings.HasSuffix(f.Kind, "."):
		q += ` AND kind LIKE ? ESCAPE '\'`
		args = append(args, escapeLike(f.Kind)+"%")
	case f.Kind != "":
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if f.Ref != "" {
		q += ` AND ref = ?`
		args = append(args, f.Ref)
	}
	if f.GroupID != "" {
		q += ` AND group_id = ?`
		args = append(args, f.GroupID)
	}
	if f.ActiveOnly {
		q += ` AND status IN ('queued', 'running')`
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Operation{}
	for rows.Next() {
		op, err := scanOp(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	for i := range out {
		if o := s.live[out[i].ID]; o != nil {
			out[i] = o.snapshot(false)
		}
	}
	s.mu.Unlock()
	if f.ActiveOnly {
		kept := out[:0]
		for _, op := range out {
			if op.Status.Active() {
				kept = append(kept, op)
			}
		}
		out = kept
	}
	return out, nil
}

// Cancel stops a queued operation whose owner registered a cancel
// function, or running work inside a window its owner opened with
// AllowCancel. Running work ends on its own once it has stopped; Finish
// then records it as cancelled.
func (s *Service) Cancel(id string) error {
	s.mu.Lock()
	o := s.live[id]
	s.mu.Unlock()
	if o == nil {
		return ErrNotCancellable
	}
	o.mu.Lock()
	cancel := o.cancel
	queued := o.op.Status == StatusQueued
	o.mu.Unlock()
	if queued && cancel != nil && cancel() {
		o.end(StatusCancelled, "cancelled before it started")
		return nil
	}
	o.mu.Lock()
	stop := o.runCancel
	if o.ended || stop == nil || o.op.CancelRequested {
		o.mu.Unlock()
		return ErrNotCancellable
	}
	o.op.CancelRequested = true
	o.op.Cancellable = false
	o.cancelStep = runningStepLabel(&o.op)
	o.mu.Unlock()
	o.Logf("warn", "cancel requested")
	stop(ErrCancelled)
	return nil
}

func runningStepLabel(op *Operation) string {
	for i := len(op.Steps) - 1; i >= 0; i-- {
		if op.Steps[i].Status == StepRunning {
			return op.Steps[i].Label
		}
	}
	return ""
}

// Subscribe returns a channel of events for every live operation. Slow
// readers miss events rather than block the work; they can reload the
// operation with Get.
func (s *Service) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(ch)
		}
		s.mu.Unlock()
	}
}

func (s *Service) publish(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// MarkInterrupted fails every operation a previous process left queued or
// running, so nothing stays stuck after a restart. Call once at startup,
// before any new operation starts.
func (s *Service) MarkInterrupted(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, steps FROM operations WHERE status IN ('queued', 'running')`)
	if err != nil {
		return 0, err
	}
	type stale struct {
		id    string
		steps []Step
	}
	var list []stale
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		var steps []Step
		_ = json.Unmarshal([]byte(raw), &steps)
		list = append(list, stale{id: id, steps: steps})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	now := s.now().UTC()
	for _, st := range list {
		for i := range st.steps {
			switch st.steps[i].Status {
			case StepRunning:
				st.steps[i].Status = StepFailed
				st.steps[i].Detail = RestartReason
				st.steps[i].FinishedAt = &now
			case StepPending:
				st.steps[i].Status = StepSkipped
			}
		}
		raw, _ := json.Marshal(st.steps)
		if _, err := s.db.ExecContext(ctx,
			`UPDATE operations SET status = 'failed', error = ?, steps = ?, finished_at = ? WHERE id = ?`,
			RestartReason, string(raw), now.UnixMilli(), st.id); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// Prune deletes ended operations created before the retention window.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	cutoff := s.now().Add(-Retention).UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM operations WHERE created_at < ? AND status NOT IN ('queued', 'running')`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// StartPruner prunes once now and then every hour until ctx ends.
func (s *Service) StartPruner(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if n, err := s.Prune(ctx); err != nil {
				slog.Warn("operations: prune", "err", err)
			} else if n > 0 {
				slog.Info("operations: pruned", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Op is the handle the working code reports through.
type Op struct {
	s *Service
	// ctx carries the starting request's values without its cancellation;
	// the record is written even after the request ended.
	ctx context.Context

	// wmu orders the database writes of this operation; mu guards op.
	wmu       sync.Mutex
	mu        sync.Mutex
	op        Operation
	secrets   []string
	cancel    func() bool
	runCancel context.CancelCauseFunc
	// cancelStep is the label of the step that ran when cancel was asked.
	cancelStep string
	lastFlush  time.Time
	ended      bool
}

func (o *Op) ID() string {
	if o == nil {
		return ""
	}
	return o.op.ID
}

// AddSecrets registers values masked from now on.
func (o *Op) AddSecrets(vals ...string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.addSecrets(vals)
	o.mu.Unlock()
}

func (o *Op) addSecrets(vals []string) {
	for _, v := range vals {
		o.secrets = appendSecret(o.secrets, v)
	}
}

// SetCancel registers how to stop the operation while it is queued.
// cancel reports false when the work has started meanwhile.
func (o *Op) SetCancel(cancel func() bool) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.cancel = cancel
	o.mu.Unlock()
}

// AllowCancel lets a user stop the running work until DisallowCancel:
// Cancel calls stop with ErrCancelled. Work that must not be interrupted
// (swapping files, replacing a container) runs outside such a window.
func (o *Op) AllowCancel(stop context.CancelCauseFunc) {
	if o == nil {
		return
	}
	o.update(func(op *Operation, _ time.Time) {
		o.runCancel = stop
		op.Cancellable = !op.CancelRequested
	})
}

// DisallowCancel closes the window opened by AllowCancel. A cancel that
// was already requested stays requested.
func (o *Op) DisallowCancel() {
	if o == nil {
		return
	}
	o.update(func(op *Operation, _ time.Time) {
		o.runCancel = nil
		op.Cancellable = false
	})
}

// CancelRequested reports whether a user asked to cancel the work.
func (o *Op) CancelRequested() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.op.CancelRequested
}

// Begin moves a queued operation to running.
func (o *Op) Begin() {
	if o == nil {
		return
	}
	o.update(func(op *Operation, now time.Time) {
		if op.Status == StatusQueued {
			op.Status = StatusRunning
			op.StartedAt = &now
		}
	})
}

// StepStart marks a step running. Unknown keys are appended, so work
// whose steps depend on what it finds can add them as it goes.
func (o *Op) StepStart(key, label string) {
	if o == nil {
		return
	}
	o.update(func(op *Operation, now time.Time) {
		st := stepFor(op, key, label)
		st.Status = StepRunning
		st.StartedAt = &now
		st.FinishedAt = nil
		st.Progress = nil
	})
}

// StepProgress reports how far a running step is (0..1) and a short
// detail such as "120 of 300 MB". It is published but written to the
// database only with the next flush.
func (o *Op) StepProgress(key string, frac float64, detail string) {
	if o == nil {
		return
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	o.mu.Lock()
	if o.ended {
		o.mu.Unlock()
		return
	}
	for i := range o.op.Steps {
		if o.op.Steps[i].Key == key {
			f := frac
			o.op.Steps[i].Progress = &f
			o.op.Steps[i].Detail = truncate(o.mask(detail))
		}
	}
	snap := o.snapshotLocked(false)
	o.mu.Unlock()
	o.s.publish(Event{Type: "update", Op: snap})
	o.maybeFlush()
}

// StepDetail updates the detail of a running step whose progress cannot
// be measured, such as the bytes written so far.
func (o *Op) StepDetail(key, detail string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.ended {
		o.mu.Unlock()
		return
	}
	for i := range o.op.Steps {
		if o.op.Steps[i].Key == key {
			o.op.Steps[i].Detail = truncate(o.mask(detail))
		}
	}
	snap := o.snapshotLocked(false)
	o.mu.Unlock()
	o.s.publish(Event{Type: "update", Op: snap})
	o.maybeFlush()
}

// StepDone ends a step successfully.
func (o *Op) StepDone(key, detail string) { o.stepEnd(key, StepSucceeded, detail) }

// StepWarn ends a step whose outcome is not confirmed.
func (o *Op) StepWarn(key, detail string) { o.stepEnd(key, StepWarning, detail) }

// StepSkip marks a step that did not apply.
func (o *Op) StepSkip(key, detail string) { o.stepEnd(key, StepSkipped, detail) }

// StepFail ends a step with an error; Finish still decides the outcome.
func (o *Op) StepFail(key string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	o.stepEnd(key, StepFailed, msg)
}

func (o *Op) stepEnd(key string, status StepStatus, detail string) {
	if o == nil {
		return
	}
	o.update(func(op *Operation, now time.Time) {
		st := stepFor(op, key, "")
		if status == StepFailed && op.CancelRequested {
			status, detail = StepCancelled, "cancelled"
		}
		st.Status = status
		st.Detail = truncate(o.mask(detail))
		if st.StartedAt == nil && status != StepSkipped {
			st.StartedAt = &now
		}
		st.FinishedAt = &now
		if status == StepSucceeded {
			one := 1.0
			st.Progress = &one
		}
	})
}

// Logf appends a log line. level is info, warn or error.
func (o *Op) Logf(level, format string, args ...any) {
	if o == nil {
		return
	}
	text := format
	if len(args) > 0 {
		text = fmt.Sprintf(format, args...)
	}
	o.mu.Lock()
	if o.ended {
		o.mu.Unlock()
		return
	}
	line := LogLine{TS: o.s.now().UTC(), Level: level, Text: truncate(o.mask(text))}
	o.op.Log = append(o.op.Log, line)
	if n := len(o.op.Log); n > MaxLogLines {
		o.op.Log = append(o.op.Log[:0:0], o.op.Log[n-MaxLogLines:]...)
	}
	snap := o.snapshotLocked(false)
	o.mu.Unlock()
	o.s.publish(Event{Type: "log", Op: snap, Line: &line})
	o.maybeFlush()
}

// Finish ends the operation: failed when err is set, else succeeded.
// Steps still running fail with err; pending steps are skipped. Work a
// user cancelled that ends with an error ends cancelled, naming the step
// it stopped in.
func (o *Op) Finish(err error) {
	if o == nil {
		return
	}
	if err != nil && o.CancelRequested() {
		o.mu.Lock()
		msg := "cancelled"
		if o.cancelStep != "" {
			msg = "cancelled during “" + o.cancelStep + "”"
		}
		o.mu.Unlock()
		o.end(StatusCancelled, msg)
		return
	}
	if err != nil {
		o.end(StatusFailed, err.Error())
		return
	}
	o.end(StatusSucceeded, "")
}

func (o *Op) end(status Status, msg string) {
	o.mu.Lock()
	if o.ended {
		o.mu.Unlock()
		return
	}
	now := o.s.now().UTC()
	o.op.Status = status
	o.op.Error = truncate(o.mask(msg))
	o.op.FinishedAt = &now
	o.op.Cancellable = false
	o.runCancel = nil
	for i := range o.op.Steps {
		st := &o.op.Steps[i]
		switch st.Status {
		case StepRunning:
			st.FinishedAt = &now
			switch status {
			case StatusSucceeded:
				st.Status = StepSucceeded
			case StatusCancelled:
				st.Status = StepCancelled
				st.Detail = "cancelled"
			default:
				st.Status = StepFailed
				if st.Detail == "" || st.Progress != nil {
					st.Detail = o.op.Error
				}
			}
		case StepPending:
			st.Status = StepSkipped
		}
	}
	o.ended = true
	snap := o.snapshotLocked(false)
	o.mu.Unlock()
	o.flush()
	o.s.mu.Lock()
	delete(o.s.live, o.op.ID)
	o.s.mu.Unlock()
	o.s.publish(Event{Type: "update", Op: snap})
}

func (o *Op) update(fn func(op *Operation, now time.Time)) {
	o.mu.Lock()
	if o.ended {
		o.mu.Unlock()
		return
	}
	fn(&o.op, o.s.now().UTC())
	snap := o.snapshotLocked(false)
	o.mu.Unlock()
	o.flush()
	o.s.publish(Event{Type: "update", Op: snap})
}

func stepFor(op *Operation, key, label string) *Step {
	for i := range op.Steps {
		if op.Steps[i].Key == key {
			return &op.Steps[i]
		}
	}
	if label == "" {
		label = key
	}
	op.Steps = append(op.Steps, Step{Key: key, Label: label, Status: StepPending})
	return &op.Steps[len(op.Steps)-1]
}

func (o *Op) snapshot(withLog bool) Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.snapshotLocked(withLog)
}

func (o *Op) snapshotLocked(withLog bool) Operation {
	op := o.op
	op.Steps = make([]Step, len(o.op.Steps))
	copy(op.Steps, o.op.Steps)
	for i := range op.Steps {
		if p := op.Steps[i].Progress; p != nil {
			v := *p
			op.Steps[i].Progress = &v
		}
	}
	op.Log = nil
	if withLog {
		op.Log = append([]LogLine{}, o.op.Log...)
	}
	return op
}

const flushEvery = time.Second

func (o *Op) maybeFlush() {
	o.mu.Lock()
	due := o.s.now().Sub(o.lastFlush) >= flushEvery
	o.mu.Unlock()
	if due {
		o.flush()
	}
}

func (o *Op) flush() {
	o.wmu.Lock()
	defer o.wmu.Unlock()
	o.mu.Lock()
	op := o.snapshotLocked(true)
	o.lastFlush = o.s.now()
	o.mu.Unlock()
	if err := o.s.save(o.ctx, &op); err != nil {
		slog.Warn("operations: save", "id", op.ID, "err", err)
	}
}

// The list reads no log column; it would load up to 500 lines per row.
const (
	selectOne  = `SELECT id, kind, target, ref, group_id, host_id, stack, admin_only, actor, status, error, steps, log, created_at, started_at, finished_at FROM operations`
	selectList = `SELECT id, kind, target, ref, group_id, host_id, stack, admin_only, actor, status, error, steps, '[]', created_at, started_at, finished_at FROM operations`
)

func (s *Service) insert(ctx context.Context, op *Operation) error {
	steps, _ := json.Marshal(op.Steps)
	_, err := s.db.ExecContext(ctx, `INSERT INTO operations
		(id, kind, target, ref, group_id, host_id, stack, admin_only, actor, status, error, steps, log, created_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, '[]', ?, ?, NULL)`,
		op.ID, op.Kind, op.Target, op.Ref, op.GroupID, op.HostID, op.Stack, boolInt(op.AdminOnly), op.Actor,
		string(op.Status), string(steps), op.CreatedAt.UnixMilli(), msPtr(op.StartedAt))
	return err
}

func (s *Service) save(ctx context.Context, op *Operation) error {
	steps, _ := json.Marshal(op.Steps)
	logs := op.Log
	if logs == nil {
		logs = []LogLine{}
	}
	raw, _ := json.Marshal(logs)
	_, err := s.db.ExecContext(ctx,
		`UPDATE operations SET status = ?, error = ?, steps = ?, log = ?, started_at = ?, finished_at = ? WHERE id = ?`,
		string(op.Status), op.Error, string(steps), string(raw), msPtr(op.StartedAt), msPtr(op.FinishedAt), op.ID)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scanOp(sc scanner, withLog bool) (*Operation, error) {
	var (
		op                  Operation
		status, steps, logs string
		adminOnly           int
		created             int64
		started, finished   sql.NullInt64
	)
	if err := sc.Scan(&op.ID, &op.Kind, &op.Target, &op.Ref, &op.GroupID, &op.HostID, &op.Stack, &adminOnly,
		&op.Actor, &status, &op.Error, &steps, &logs, &created, &started, &finished); err != nil {
		return nil, err
	}
	op.Status = Status(status)
	op.AdminOnly = adminOnly != 0
	op.CreatedAt = time.UnixMilli(created).UTC()
	op.StartedAt = timePtr(started)
	op.FinishedAt = timePtr(finished)
	if err := json.Unmarshal([]byte(steps), &op.Steps); err != nil || op.Steps == nil {
		op.Steps = []Step{}
	}
	if withLog {
		_ = json.Unmarshal([]byte(logs), &op.Log)
	}
	return &op, nil
}

// NewGroupID returns an id that ties operations started together.
func NewGroupID() string { return newID() }

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("op%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func truncate(s string) string {
	const max = MaxLineBytes
	if len(s) <= max {
		return s
	}
	cut := max - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func msPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

type ctxKey struct{}
type actorKey struct{}

// WithOp attaches an operation to ctx for the code doing the work.
func WithOp(ctx context.Context, o *Op) context.Context {
	if o == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, o)
}

// FromContext returns the operation attached to ctx, or nil.
func FromContext(ctx context.Context) *Op {
	o, _ := ctx.Value(ctxKey{}).(*Op)
	return o
}

// WithActor records who asked for work that starts its operation later,
// such as a backup run started in the background.
func WithActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

func ActorFromContext(ctx context.Context) string {
	s, _ := ctx.Value(actorKey{}).(string)
	return s
}
