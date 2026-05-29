package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/stacks"
	dtypes "github.com/docker/docker/api/types"
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
	if err := e.store.appendRunLog(ctx, runID, stream, line); err != nil {
		slog.Debug("backup runlog append failed", "run", runID, "err", err)
	}
}

// Run executes one backup job, persisting a backup_runs row throughout.
// Returns the final run record.
func (e *Executor) Run(ctx context.Context, job *Job) (*Run, error) {
	runID, err := e.store.startRun(ctx, job)
	if err != nil {
		return nil, err
	}
	e.logf(ctx, runID, "info", "start: job=%s host=%s sources=%d target=%s encrypt=%v",
		job.Name, defaultStr(job.HostID, "local"), len(job.Sources), job.TargetType, job.Encrypt)

	// Multi-host backup: resolve the target host once so the source
	// extractors + pre-hooks go against the right Docker daemon.
	hostTarget, err := e.resolveHost(job.HostID)
	if err != nil {
		e.logf(ctx, runID, "error", "resolve host: %s", err)
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", err)
		return e.store.getRun(ctx, runID)
	}

	target, err := buildTarget(job.TargetType, job.TargetConfig)
	if err != nil {
		e.logf(ctx, runID, "error", "build target: %s", err)
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", err)
		return e.store.getRun(ctx, runID)
	}

	if err := e.runHooks(ctx, runID, "pre", hostTarget, job.PreHooks); err != nil {
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", fmt.Errorf("pre-hooks: %w", err))
		return e.store.getRun(ctx, runID)
	}

	relPath := relativePath(job, runID)
	w, err := target.Open(ctx, relPath)
	if err != nil {
		e.logf(ctx, runID, "error", "open target %q: %s", relPath, err)
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", err)
		return e.store.getRun(ctx, runID)
	}

	hasher := sha256.New()
	teeW := newTeeWriter(w, hasher)

	encWriter, err := wrapEncrypt(teeW, encryptionFor(job, e.secrets))
	if err != nil {
		e.logf(ctx, runID, "error", "wrap encrypt: %s", err)
		_ = w.Close()
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", err)
		return e.store.getRun(ctx, runID)
	}
	e.logf(ctx, runID, "info", "streaming %d source(s) → %s", len(job.Sources), relPath)

	written, copyErr := e.streamSources(ctx, job.Sources, hostTarget, encWriter)
	closeErr := encWriter.Close()
	if copyErr == nil && closeErr == nil {
		e.logf(ctx, runID, "info", "wrote %d bytes", teeW.size)
	}
	hookErr := e.runHooks(ctx, runID, "post", hostTarget, job.PostHooks)

	var finalStatus string
	var finalErr error
	switch {
	case copyErr != nil:
		e.logf(ctx, runID, "error", "stream sources: %s", copyErr)
		_ = e.store.finishRun(ctx, runID, "failed", teeW.size, relPath, "", copyErr)
		finalStatus, finalErr = "failed", copyErr
	case closeErr != nil:
		e.logf(ctx, runID, "error", "close encrypted writer: %s", closeErr)
		_ = e.store.finishRun(ctx, runID, "failed", teeW.size, relPath, "", closeErr)
		finalStatus, finalErr = "failed", closeErr
	case hookErr != nil:
		// Backup itself succeeded; surface hook failure but keep the file.
		sum := hex.EncodeToString(hasher.Sum(nil))
		e.logf(ctx, runID, "warn", "post-hooks failed but backup file is intact: %s", hookErr)
		wrapped := fmt.Errorf("post-hooks: %w", hookErr)
		_ = e.store.finishRun(ctx, runID, "failed", teeW.size, relPath, sum, wrapped)
		finalStatus, finalErr = "failed", wrapped
	default:
		sum := hex.EncodeToString(hasher.Sum(nil))
		e.logf(ctx, runID, "info", "success: size=%d sha256=%s", teeW.size, sum[:12])
		_ = e.store.finishRun(ctx, runID, "success", teeW.size, relPath, sum, nil)
		if err := e.applyRetention(ctx, job, target); err != nil {
			slog.Warn("backup retention", "job", job.Name, "err", err)
			e.logf(ctx, runID, "warn", "retention pass: %s", err)
		}
		finalStatus = "success"
	}
	_ = written

	now := time.Now()
	_ = e.store.updateJobRunTimes(ctx, job.ID, &now, nil)

	// Bell-icon notification — fan-out so every operator sees the run
	// in their feed. Failures get error severity, success info.
	if e.notifs != nil {
		runLink := fmt.Sprintf("/backups/runs/%d", runID)
		if finalStatus == "success" {
			_, _ = e.notifs.Emit(ctx, notifications.EmitInput{
				Kind:     notifications.KindBackupOK,
				Severity: notifications.SevSuccess,
				Title:    "Backup completed: " + job.Name,
				Body:     fmt.Sprintf("Wrote %d bytes", teeW.size),
				Link:     runLink,
			})
		} else {
			body := "Run failed"
			if finalErr != nil {
				body = finalErr.Error()
			}
			_, _ = e.notifs.Emit(ctx, notifications.EmitInput{
				Kind:     notifications.KindBackupFail,
				Severity: notifications.SevError,
				Title:    "Backup failed: " + job.Name,
				Body:     body,
				Link:     runLink,
			})
		}
	}
	return e.store.getRun(ctx, runID)
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

func (e *Executor) runHooks(ctx context.Context, runID int64, phase string, hostTarget hostBackupTarget, hooks []Hook) error {
	if len(hooks) == 0 || hostTarget == nil {
		return nil
	}
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
			return fmt.Errorf("exec %s: %w", h.Container, err)
		}
		if exitCode != 0 {
			e.logf(ctx, runID, "error", "%s-hook exit code %d", phase, exitCode)
			return fmt.Errorf("exec %s exit %d", h.Container, exitCode)
		}
	}
	return nil
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

func (l *localBackupTarget) ContainerExec(ctx context.Context, containerID string, cmd []string) ([]byte, int, error) {
	cli := l.dc.Raw()
	exec, err := cli.ContainerExecCreate(ctx, containerID, dtypes.ExecConfig{
		Cmd: cmd, AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return nil, -1, err
	}
	attach, err := cli.ContainerExecAttach(ctx, exec.ID, dtypes.ExecStartCheck{})
	if err != nil {
		return nil, -1, err
	}
	defer attach.Close()
	var out bytes.Buffer
	_, _ = io.CopyN(&out, attach.Reader, 1<<20)
	insp, inspErr := cli.ContainerExecInspect(ctx, exec.ID)
	if inspErr != nil {
		return out.Bytes(), -1, inspErr
	}
	return out.Bytes(), insp.ExitCode, nil
}

// applyRetention deletes runs older than RetentionDays AND keeps only
// the newest RetentionCount entries. 0 disables the respective rule.
func (e *Executor) applyRetention(ctx context.Context, job *Job, target targets.Target) error {
	if job.RetentionCount <= 0 && job.RetentionDays <= 0 {
		return nil
	}
	prefix := jobPrefix(job)
	entries, err := target.List(ctx, prefix)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ModTime.After(entries[j].ModTime)
	})
	now := time.Now()
	for i, e2 := range entries {
		drop := false
		if job.RetentionCount > 0 && i >= job.RetentionCount {
			drop = true
		}
		if !drop && job.RetentionDays > 0 && now.Sub(e2.ModTime) > time.Duration(job.RetentionDays)*24*time.Hour {
			drop = true
		}
		if drop {
			if err := target.Delete(ctx, e2.Path); err != nil {
				slog.Warn("retention delete", "path", e2.Path, "err", err)
			}
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

type teeWriter struct {
	w    io.Writer
	hash io.Writer
	size int64
}

func newTeeWriter(w io.Writer, h io.Writer) *teeWriter {
	return &teeWriter{w: w, hash: h}
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	if n > 0 {
		t.hash.Write(p[:n])
		t.size += int64(n)
	}
	return n, err
}

func (t *teeWriter) Close() error {
	if c, ok := t.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func relativePath(job *Job, runID int64) string {
	stamp := time.Now().UTC().Format("20060102-150405")
	ext := ".tar.gz"
	if job.Encrypt {
		ext += ".age"
	}
	return fmt.Sprintf("%s/%s-r%d%s", jobPrefix(job), stamp, runID, ext)
}

func jobPrefix(job *Job) string {
	return "jobs/" + sanitize(job.Name)
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

