package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// Executor turns a Job into a Run: pre-hooks → tar → encrypt → upload →
// post-hooks → retention. One method per phase keeps the orchestration
// readable.
type Executor struct {
	store   *store
	db      *sql.DB
	docker  *docker.Client
	hosts   hostResolver
	stacks  *stacks.Manager
	secrets *secrets.Service
	paths   SystemPaths
	notifs  *notifications.Service
	notices *notices.Service
	ops     *operations.Service

	// runOps carries the operation of a run from begin to complete.
	runOps sync.Map
}

// Steps of a backup run, in order, as reported to its operation.
const (
	StepPrepare   = "prepare"
	StepPreHooks  = "pre_hooks"
	StepArchive   = "archive"
	StepVerify    = "verify"
	StepPostHooks = "post_hooks"
	StepRetention = "retention"
)

var runSteps = []operations.StepSpec{
	{Key: StepPrepare, Label: "Connect to host and target"},
	{Key: StepPreHooks, Label: "Run pre-backup hooks"},
	{Key: StepArchive, Label: "Archive and upload"},
	{Key: StepVerify, Label: "Read back and verify"},
	{Key: StepPostHooks, Label: "Run post-backup hooks"},
	{Key: StepRetention, Label: "Apply retention"},
}

// hostResolver lets the executor route to local vs remote hosts without
// importing internal/host (that would create a cycle — host imports
// internal/compose which we also need).
type hostResolver interface {
	Pick(id string) (hostBackupTarget, error)
}

// hostBackupTarget is the subset of host.Host the executor needs.
type hostBackupTarget interface {
	VolumeTar(ctx context.Context, name string) (io.ReadCloser, error)
	ContainerExec(ctx context.Context, containerID string, cmd []string) ([]byte, int, error)
	// VolumeImport extracts a tar.gz archive into the volume; replace
	// swaps the volume's contents for the archive's once it extracted
	// completely, needing needBytes of free space.
	VolumeImport(ctx context.Context, name string, src io.Reader, replace bool, needBytes int64) error
	// VolumeInUse names the running containers that mount the volume.
	VolumeInUse(ctx context.Context, name string) ([]string, error)
}

func newExecutor(s *store, db *sql.DB, dc *docker.Client, hosts hostResolver, sm *stacks.Manager, sec *secrets.Service, paths SystemPaths) *Executor {
	return &Executor{store: s, db: db, docker: dc, hosts: hosts, stacks: sm, secrets: sec, paths: paths}
}

// logf appends one captured line to the run-log. Best-effort: if the
// DB insert fails we drop the line into slog instead of failing the
// backup. The store is small + write-mostly so failures are rare.
func (e *Executor) logf(ctx context.Context, runID int64, stream, format string, args ...any) {
	line := format
	if len(args) > 0 {
		line = fmt.Sprintf(format, args...)
	}
	// Lines about a cancelled or stalled run are written after its
	// context ended.
	if err := e.store.appendRunLog(context.WithoutCancel(ctx), runID, stream, line); err != nil {
		slog.Debug("backup runlog append failed", "run", runID, "err", err)
	}
	level := stream
	switch stream {
	case "hook_stdout":
		level = "info"
	case "hook_stderr":
		level = "warn"
	}
	operations.FromContext(ctx).Logf(level, "%s", line)
}

// Run executes one backup job, persisting a backup_runs row throughout.
// Returns the final run record.
func (e *Executor) Run(ctx context.Context, job *Job) (*Run, error) {
	runID, err := e.begin(ctx, job)
	if err != nil {
		return nil, err
	}
	return e.complete(ctx, job, runID)
}

// begin records a new run in state running.
func (e *Executor) begin(ctx context.Context, job *Job) (int64, error) {
	runID, err := e.store.startRun(ctx, job)
	if err != nil {
		return 0, err
	}
	op := e.startOp(ctx, job, runID)
	e.runOps.Store(runID, op)
	ctx = operations.WithOp(ctx, op)
	e.logf(ctx, runID, "info", "start: job=%s host=%s sources=%d target=%s encrypt=%v",
		job.Name, defaultStr(job.HostID, "local"), len(job.Sources), job.TargetType, job.Encrypt)
	return runID, nil
}

// complete executes a run started with begin and records its outcome.
func (e *Executor) complete(ctx context.Context, job *Job, runID int64) (*Run, error) {
	var op *operations.Op
	if v, ok := e.runOps.LoadAndDelete(runID); ok {
		op = v.(*operations.Op)
	}
	ctx = operations.WithOp(ctx, op)
	runCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	op.AllowCancel(stop)
	out, target := e.execute(runCtx, job, runID, stop)
	op.DisallowCancel()
	// The outcome is recorded even when the caller went away mid-run.
	ctx = context.WithoutCancel(ctx)
	if err := e.store.finishRun(ctx, runID, out.status, out.size, out.path, out.sum, out.err); err != nil {
		slog.Warn("backup finish run", "run", runID, "err", err)
	}
	if target != nil {
		op.StepStart(StepRetention, "")
		rctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
		e.applyRetention(rctx, runID, job, target)
		cancel()
		op.StepDone(StepRetention, "")
	}

	now := time.Now()
	_ = e.store.updateJobRunTimes(ctx, job.ID, &now, nil)
	e.notifyRun(ctx, job, runID, out)
	if out.status == StatusFailed {
		op.Finish(out.err)
	} else {
		op.Finish(nil)
	}
	return e.store.getRun(ctx, runID)
}

// runOutcome is what execute hands back for the backup_runs row. err is
// the failure, or the warning of a success_with_warnings run.
type runOutcome struct {
	status string
	size   int64
	path   string
	sum    string
	err    error
}

// execute runs the phases of one backup. The target comes back only when
// the run produced an archive, so retention never runs after a failure.
// ctx ends when a user cancels the run or the transfer stalls; the
// post-hooks run either way.
func (e *Executor) execute(ctx context.Context, job *Job, runID int64, stop context.CancelCauseFunc) (out runOutcome, kept targets.Target) {
	op := operations.FromContext(ctx)
	step := StepPrepare
	fail := func(stage string, err error) runOutcome {
		if cause := stopCause(ctx); cause != nil {
			err = cause
		}
		e.logf(ctx, runID, "error", "%s: %s", stage, err)
		err = fmt.Errorf("%s: %w", stage, err)
		op.StepFail(step, err)
		return runOutcome{status: StatusFailed, err: err}
	}

	op.StepStart(StepPrepare, "")
	resolved, err := e.resolveHost(job.HostID)
	if err != nil {
		return fail("resolve host", err), nil
	}
	hostTarget := hostBackupTarget(stoppableHost{resolved})
	if job.Encrypt && !canEncrypt(e.secrets) {
		e.logf(ctx, runID, "error", "%s", ErrEncryptUnavailable)
		op.StepFail(step, ErrEncryptUnavailable)
		return runOutcome{status: StatusFailed, err: ErrEncryptUnavailable}, nil
	}
	target, err := jobTarget(ctx, e.store, e.notices, job)
	if err != nil {
		return fail("build target", err), nil
	}
	op.StepDone(StepPrepare, "")

	// Pre-hooks typically stop or lock a service, so once one of them has
	// run, the post-hooks run too, whatever happens afterwards.
	step = StepPreHooks
	if len(job.PreHooks) == 0 {
		op.StepSkip(StepPreHooks, "none configured")
	} else {
		op.StepStart(StepPreHooks, "")
	}
	preRan, preErr := e.runHooks(ctx, runID, "pre", hostTarget, job.PreHooks)
	if preErr == nil || preRan > 0 {
		defer func() { out = e.runPostHooks(ctx, runID, hostTarget, job, out) }()
	}
	if preErr != nil {
		return fail("pre-hooks", preErr), nil
	}
	if len(job.PreHooks) > 0 {
		op.StepDone(StepPreHooks, fmt.Sprintf("%d ran", preRan))
	}
	step = StepArchive
	op.StepStart(StepArchive, "")

	act := newActivity()
	defer watchIdle(act, stop)()
	relPath := relativePath(job, runID)
	// Recorded up front so the partial file of a crashed run stays
	// traceable.
	if err := e.store.setRunPath(ctx, runID, relPath); err != nil {
		slog.Warn("backup record run path", "run", runID, "err", err)
	}
	w, err := target.Open(ctx, relPath)
	if err != nil {
		return fail(fmt.Sprintf("open target %q", relPath), err), nil
	}
	// The hash covers the bytes as stored on the target (ciphertext for
	// encrypted runs), which read-back, verify and restore check against.
	hasher := sha256.New()
	stored := &countingWriter{w: w, h: hasher, act: act}
	// Unblocks a write to a target that stopped answering.
	stopAbort := closeOnDone(ctx, stored.Abort)
	encW, err := wrapEncrypt(stored, encryptionFor(job, e.secrets))
	if err != nil {
		stopAbort()
		_ = abortWriter(stored)
		out = fail("encrypt", err)
		out.path = e.removePartial(ctx, runID, target, w, relPath, false)
		return out, nil
	}

	e.logf(ctx, runID, "info", "streaming %d source(s) → %s", len(job.Sources), relPath)
	if _, err := e.streamSources(ctx, job.Sources, hostTarget, &progressWriter{w: encW, op: op, step: StepArchive, act: act}); err != nil {
		stopAbort()
		_ = abortWriter(encW)
		out = fail("stream sources", err)
		out.path = e.removePartial(ctx, runID, target, w, relPath, false)
		return out, nil
	}
	if err := encW.Close(); err != nil {
		stopAbort()
		out = fail("finish upload", err)
		out.path = e.removePartial(ctx, runID, target, w, relPath, true)
		return out, nil
	}
	if !stopAbort() {
		// Cancelled or stalled while the upload was closing: the abort may
		// have cut it short, so the archive is not trusted.
		out = fail("finish upload", context.Cause(ctx))
		out.path = e.removePartial(ctx, runID, target, w, relPath, true)
		return out, nil
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	e.logf(ctx, runID, "info", "wrote %d bytes, sha256=%s", stored.n, sum[:12])
	op.StepDone(StepArchive, operations.HumanBytes(stored.n)+" written")
	op.StepStart(StepVerify, "")
	if err := readBack(ctx, target, relPath, stored.n, sum, act); err != nil {
		if cause := stopCause(ctx); cause != nil {
			err = fmt.Errorf("read back: %w", cause)
		}
		e.logf(ctx, runID, "error", "%s", err)
		op.StepFail(StepVerify, err)
		out = runOutcome{status: StatusFailed, size: stored.n, err: err}
		out.path = e.removePartial(ctx, runID, target, w, relPath, true)
		return out, nil
	}
	e.logf(ctx, runID, "info", "read back %d bytes, sha256 matches", stored.n)
	op.StepDone(StepVerify, "checksum matches")
	return runOutcome{status: StatusSuccess, size: stored.n, path: relPath, sum: sum}, target
}

// runPostHooks uses a context that survives cancellation of the run: a
// service stopped by a pre-hook must come back up either way.
func (e *Executor) runPostHooks(ctx context.Context, runID int64, hostTarget hostBackupTarget, job *Job, out runOutcome) runOutcome {
	op := operations.FromContext(ctx)
	if len(job.PostHooks) == 0 {
		op.StepSkip(StepPostHooks, "none configured")
	} else {
		op.StepStart(StepPostHooks, "")
	}
	ran, err := e.runHooks(context.WithoutCancel(ctx), runID, "post", hostTarget, job.PostHooks)
	switch {
	case len(job.PostHooks) == 0:
	case err != nil:
		op.StepWarn(StepPostHooks, err.Error())
	default:
		op.StepDone(StepPostHooks, fmt.Sprintf("%d ran", ran))
	}
	if err == nil || out.status != StatusSuccess {
		return out
	}
	e.logf(ctx, runID, "warn", "post-hooks failed, the archive is complete and restorable: %s", err)
	out.status = StatusSuccessWithWarnings
	out.err = fmt.Errorf("post-hooks: %w", err)
	return out
}

// cleanupTimeout bounds retention and partial-file deletes, which run
// after the caller may have gone away; a stalled target must not keep
// them alive forever.
const cleanupTimeout = 10 * time.Minute

// removePartial deletes what a failed run wrote. Targets that upload on
// Close published nothing when the run was aborted before Close (closed
// false). Returns the path to keep on the run when the file could not be
// deleted, so the retention pass retries.
func (e *Executor) removePartial(ctx context.Context, runID int64, target targets.Target, w io.WriteCloser, relPath string, closed bool) string {
	if _, ok := w.(targets.Aborter); ok && !closed {
		e.logf(ctx, runID, "info", "the target dropped the unfinished upload of %s", relPath)
		return ""
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	err := target.Delete(dctx, relPath)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		e.logf(ctx, runID, "info", "deleted the partial archive %s", relPath)
		return ""
	}
	e.logf(ctx, runID, "warn", "could not delete partial archive %s: %s", relPath, err)
	return relPath
}

func (e *Executor) notifyRun(ctx context.Context, job *Job, runID int64, out runOutcome) {
	if e.notifs == nil {
		return
	}
	in := runNotification(job, runID)
	switch out.status {
	case StatusSuccess:
		in.Kind, in.Severity = notifications.KindBackupOK, notifications.SevSuccess
		in.Title = "Backup completed: " + job.Name
		in.Body = fmt.Sprintf("Wrote %d bytes", out.size)
	case StatusSuccessWithWarnings:
		in.Kind, in.Severity = notifications.KindBackupOK, notifications.SevWarning
		in.Title = "Backup completed with warnings: " + job.Name
		in.Body = out.err.Error()
	default:
		in.Kind, in.Severity = notifications.KindBackupFail, notifications.SevError
		in.Title = "Backup failed: " + job.Name
		in.Body = "Run failed"
		if out.err != nil {
			in.Body = out.err.Error()
		}
		if errors.Is(out.err, operations.ErrCancelled) {
			in.Severity = notifications.SevWarning
			in.Title = "Backup cancelled: " + job.Name
		}
	}
	_, _ = e.notifs.Emit(ctx, in)
}

// runNotification addresses a run notification to the users who may open
// the run: unscoped admins for system jobs, otherwise backup viewers whose
// scope covers the job's host.
func runNotification(job *Job, runID int64) notifications.EmitInput {
	in := notifications.EmitInput{
		Link:   fmt.Sprintf("/backups/runs/%d", runID),
		Perm:   rbac.PermBackupsView,
		HostID: defaultStr(job.HostID, "local"),
	}
	if HasSystemSource(job.Sources) {
		in.Perm = rbac.PermSystemUpdate
		in.Unscoped = true
	}
	return in
}

func defaultStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// streamSources writes the tar stream for the (single) source into w.
// validateJob enforces exactly one source per job at the API boundary,
// so multi-source jobs never reach this point — they're rejected on
// CreateJob/UpdateJob with a clear "one source per job" error. P.13.5.
func (e *Executor) streamSources(ctx context.Context, sources []Source, hostTarget hostBackupTarget, w io.Writer) (int64, error) {
	var total int64
	if len(sources) != 1 {
		return 0, fmt.Errorf("expected exactly one source, got %d (validateJob should have rejected this)", len(sources))
	}

	src := sources[0]
	switch src.Type {
	case "volume":
		rc, err := hostTarget.VolumeTar(ctx, src.Name)
		if err != nil {
			return 0, fmt.Errorf("tar volume %s: %w", src.Name, err)
		}
		defer rc.Close()
		n, err := io.Copy(w, rc)
		total += n
		return total, err
	case "stack":
		// Tar the on-disk stack dir (central server) AND every named
		// volume the compose references (pulled from hostTarget — the
		// stack's deployment host, which may be a remote agent).
		dir, err := e.stacks.Dir(src.Name)
		if err != nil {
			return 0, fmt.Errorf("stack dir: %w", err)
		}
		return tarStackWithVolumes(ctx, hostTarget, dir, src.Name, w)
	case "system":
		// System backup always lives on the central server, not on any
		// agent. Host routing is irrelevant here.
		return tarSystem(ctx, e.db, e.paths, w)
	default:
		return 0, fmt.Errorf("%w: %s", ErrUnknownSourceType, src.Type)
	}
}

// runHooks runs the hooks in order and stops at the first failure.
// Returns how many hooks completed successfully.
func (e *Executor) runHooks(ctx context.Context, runID int64, phase string, hostTarget hostBackupTarget, hooks []Hook) (int, error) {
	if len(hooks) == 0 || hostTarget == nil {
		return 0, nil
	}
	ran := 0
	e.logf(ctx, runID, "info", "%s-hooks: %d to run", phase, len(hooks))
	for _, h := range hooks {
		if h.Container == "" || len(h.Cmd) == 0 {
			continue
		}
		e.logf(ctx, runID, "info", "%s-hook: container=%s cmd=%s",
			phase, h.Container, strings.Join(h.Cmd, " "))
		hookCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		out, exitCode, err := hostTarget.ContainerExec(hookCtx, h.Container, h.Cmd)
		cancel()
		// Capture up to ~16KB of output across stream types so noisy
		// hooks don't blow the run-log table. Split on lines so the UI
		// can render them as ordered entries.
		if len(out) > 0 {
			snippet := out
			if len(snippet) > 16*1024 {
				snippet = snippet[:16*1024]
				e.logf(ctx, runID, "info", "%s-hook output truncated at 16 KB", phase)
			}
			for _, line := range strings.Split(strings.TrimRight(string(snippet), "\n"), "\n") {
				if line == "" {
					continue
				}
				stream := "hook_stdout"
				if exitCode != 0 || err != nil {
					stream = "hook_stderr"
				}
				e.logf(ctx, runID, stream, "%s", line)
			}
		}
		if err != nil {
			e.logf(ctx, runID, "error", "%s-hook exec error: %s", phase, err)
			return ran, fmt.Errorf("exec %s: %w", h.Container, err)
		}
		if exitCode != 0 {
			e.logf(ctx, runID, "error", "%s-hook exit code %d", phase, exitCode)
			return ran, fmt.Errorf("exec %s exit %d", h.Container, exitCode)
		}
		ran++
	}
	return ran, nil
}

// resolveHost returns a hostBackupTarget for the job's host_id. Empty
// or "local" returns a wrapper around the central docker client.
// Anything else is looked up in the host registry (agent connection).
func (e *Executor) resolveHost(hostID string) (hostBackupTarget, error) {
	if hostID == "" || hostID == "local" {
		if e.docker == nil {
			return nil, fmt.Errorf("local docker unavailable")
		}
		return &localBackupTarget{dc: e.docker}, nil
	}
	if e.hosts == nil {
		return nil, fmt.Errorf("host registry not configured — backup of remote hosts unavailable")
	}
	return e.hosts.Pick(hostID)
}

// localBackupTarget adapts the central docker client to hostBackupTarget.
type localBackupTarget struct {
	dc *docker.Client
}

func (l *localBackupTarget) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	return tarVolume(ctx, l.dc, name)
}

func (l *localBackupTarget) VolumeImport(ctx context.Context, name string, src io.Reader, replace bool, needBytes int64) error {
	return untarVolume(ctx, l.dc, name, src, replace, needBytes)
}

func (l *localBackupTarget) VolumeInUse(ctx context.Context, name string) ([]string, error) {
	// Paused and restarting containers come back while the volume is
	// rewritten, so only stopped ones don't count.
	list, err := l.dc.Raw().ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("volume", name)),
	})
	if err != nil {
		return nil, err
	}
	active := list[:0]
	for _, c := range list {
		if c.State != container.StateExited && c.State != container.StateDead && c.State != container.StateCreated {
			active = append(active, c)
		}
	}
	return containerNames(active), nil
}

func containerNames(list []container.Summary) []string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, name)
	}
	return out
}

func (l *localBackupTarget) ContainerExec(ctx context.Context, containerID string, cmd []string) ([]byte, int, error) {
	return docker.RunExec(ctx, l.dc.Raw(), containerID, cmd, 1<<20)
}

// applyRetention deletes archives beyond the newest RetentionCount and
// older than RetentionDays (0 disables a rule). It works from the job's
// own run records, never from a listing of the target, and always keeps
// the newest archive. Leftover partial files of failed runs go too.
func (e *Executor) applyRetention(ctx context.Context, runID int64, job *Job, target targets.Target) {
	partial, err := e.store.partialRuns(ctx, job.ID)
	if err != nil {
		e.logf(ctx, runID, "warn", "retention: %s", err)
		return
	}
	for _, r := range partial {
		if err := e.deleteArchive(ctx, target, r); err != nil {
			e.logf(ctx, runID, "warn", "retention: delete partial archive of run %d: %s", r.ID, err)
		}
	}
	if job.RetentionCount <= 0 && job.RetentionDays <= 0 {
		return
	}
	kept, err := e.store.keptRuns(ctx, job.ID)
	if err != nil {
		e.logf(ctx, runID, "warn", "retention: %s", err)
		return
	}
	maxAge := time.Duration(job.RetentionDays) * 24 * time.Hour
	now := time.Now()
	for i, r := range kept {
		if i == 0 {
			continue
		}
		drop := job.RetentionCount > 0 && i >= job.RetentionCount
		if job.RetentionDays > 0 && r.FinishedAt != nil && now.Sub(*r.FinishedAt) > maxAge {
			drop = true
		}
		if !drop {
			continue
		}
		if err := e.deleteArchive(ctx, target, r); err != nil {
			e.logf(ctx, runID, "warn", "retention: delete %s: %s", r.TargetPath, err)
			continue
		}
		e.logf(ctx, runID, "info", "retention: removed archive of run %d (%s)", r.ID, r.TargetPath)
	}
}

func (e *Executor) deleteArchive(ctx context.Context, target targets.Target, r Run) error {
	if err := target.Delete(ctx, r.TargetPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return e.store.markPruned(ctx, r.ID)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// countingWriter counts and hashes the bytes stored on the target.
type countingWriter struct {
	w   io.WriteCloser
	h   hash.Hash
	n   int64
	act *activity
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.h.Write(p[:n])
	if n > 0 {
		c.act.touch()
	}
	return n, err
}

func (c *countingWriter) Close() error { return c.w.Close() }
func (c *countingWriter) Abort() error { return abortWriter(c.w) }

func relativePath(job *Job, runID int64) string {
	stamp := time.Now().UTC().Format("20060102-150405")
	ext := ".tar.gz"
	if job.Encrypt {
		ext += ".age"
	}
	return fmt.Sprintf("%s/%s-r%d%s", jobPrefix(job), stamp, runID, ext)
}

// jobPrefix is keyed by the job ID: names can collide once sanitized and
// can change. The slug is only there for people browsing the target.
func jobPrefix(job *Job) string {
	dir := strconv.FormatInt(job.ID, 10)
	if slug := sanitize(job.Name); slug != "" {
		dir += "-" + slug
	}
	return "jobs/" + dir
}

func sanitize(s string) string {
	// Replace anything that isn't a-z0-9- with -.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+32)
		default:
			out = append(out, '-')
		}
	}
	return strings.Trim(string(out), "-")
}

func encryptionFor(job *Job, sec *secrets.Service) *secrets.Service {
	if !job.Encrypt {
		return nil
	}
	return sec
}

// startOp records the operation of a run. Scheduled runs have no actor.
func (e *Executor) startOp(ctx context.Context, job *Job, runID int64) *operations.Op {
	if e.ops == nil {
		return nil
	}
	spec := operations.Spec{
		Kind:      operations.KindBackupRun,
		Target:    job.Name,
		Ref:       operations.BackupRunRef(runID),
		HostID:    defaultStr(job.HostID, "local"),
		AdminOnly: HasSystemSource(job.Sources),
		Actor:     operations.ActorFromContext(ctx),
		Steps:     runSteps,
	}
	if len(job.Sources) == 1 && job.Sources[0].Type == "stack" {
		spec.Stack = job.Sources[0].Name
	}
	spec.Secrets = e.sourceSecrets(job.Sources)
	return e.ops.Start(ctx, spec)
}

// sourceSecrets returns the .env values of the stacks a job backs up;
// hook output may print them.
func (e *Executor) sourceSecrets(sources []Source) []string {
	if e.stacks == nil {
		return nil
	}
	var out []string
	for _, src := range sources {
		if src.Type != "stack" {
			continue
		}
		if d, err := e.stacks.Get(src.Name); err == nil && d != nil {
			out = append(out, operations.EnvValues(d.Env)...)
		}
	}
	return out
}

// progressWriter reports the bytes archived so far to the operation; the
// total is unknown until the sources are read to the end.
type progressWriter struct {
	w    io.Writer
	op   *operations.Op
	step string
	act  *activity
	n    int64
	last time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if n > 0 {
		p.act.touch()
	}
	if p.op != nil && time.Since(p.last) >= time.Second {
		p.last = time.Now()
		p.op.StepDetail(p.step, operations.HumanBytes(p.n)+" archived")
	}
	return n, err
}
