package backup

import (
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
	stacks  *stacks.Manager
	secrets *secrets.Service
	paths   SystemPaths
}

func newExecutor(s *store, db *sql.DB, dc *docker.Client, sm *stacks.Manager, sec *secrets.Service, paths SystemPaths) *Executor {
	return &Executor{store: s, db: db, docker: dc, stacks: sm, secrets: sec, paths: paths}
}

// Run executes one backup job, persisting a backup_runs row throughout.
// Returns the final run record.
func (e *Executor) Run(ctx context.Context, job *Job) (*Run, error) {
	runID, err := e.store.startRun(ctx, job)
	if err != nil {
		return nil, err
	}

	target, err := buildTarget(job.TargetType, job.TargetConfig)
	if err != nil {
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", err)
		return e.store.getRun(ctx, runID)
	}

	if err := e.runHooks(ctx, job.PreHooks); err != nil {
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", fmt.Errorf("pre-hooks: %w", err))
		return e.store.getRun(ctx, runID)
	}

	relPath := relativePath(job, runID)
	w, err := target.Open(ctx, relPath)
	if err != nil {
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", err)
		return e.store.getRun(ctx, runID)
	}

	hasher := sha256.New()
	teeW := newTeeWriter(w, hasher)

	encWriter, err := wrapEncrypt(teeW, encryptionFor(job, e.secrets))
	if err != nil {
		_ = w.Close()
		_ = e.store.finishRun(ctx, runID, "failed", 0, "", "", err)
		return e.store.getRun(ctx, runID)
	}

	written, copyErr := e.streamSources(ctx, job.Sources, encWriter)
	closeErr := encWriter.Close()
	hookErr := e.runHooks(ctx, job.PostHooks)

	switch {
	case copyErr != nil:
		_ = e.store.finishRun(ctx, runID, "failed", teeW.size, relPath, "", copyErr)
	case closeErr != nil:
		_ = e.store.finishRun(ctx, runID, "failed", teeW.size, relPath, "", closeErr)
	case hookErr != nil:
		// Backup itself succeeded; surface hook failure but keep the file.
		sum := hex.EncodeToString(hasher.Sum(nil))
		_ = e.store.finishRun(ctx, runID, "failed", teeW.size, relPath, sum,
			fmt.Errorf("post-hooks: %w", hookErr))
	default:
		sum := hex.EncodeToString(hasher.Sum(nil))
		_ = e.store.finishRun(ctx, runID, "success", teeW.size, relPath, sum, nil)
		if err := e.applyRetention(ctx, job, target); err != nil {
			slog.Warn("backup retention", "job", job.Name, "err", err)
		}
	}
	_ = written

	now := time.Now()
	_ = e.store.updateJobRunTimes(ctx, job.ID, &now, nil)
	return e.store.getRun(ctx, runID)
}

// streamSources writes one tar stream per source into w. For multiple
// sources we emit a top-level tar that contains per-source nested
// archives — each inner stream is itself a gzipped tar from the helper.
//
// For a single source we just stream its tar directly so simple jobs
// stay one well-known archive at the destination.
func (e *Executor) streamSources(ctx context.Context, sources []Source, w io.Writer) (int64, error) {
	var total int64
	if len(sources) == 0 {
		return 0, fmt.Errorf("no sources configured")
	}

	// Multiple sources: concat with a small header file marker. Simpler
	// than embedded tar-of-tars for MVP; restores are run one-source-at-
	// a-time so we just stream each volume as its own .tar.gz wrapped in
	// a tar shell. For the MVP we require exactly one source per job and
	// document multi-source as a follow-up.
	if len(sources) > 1 {
		return 0, fmt.Errorf("multiple sources per job not supported yet — create one job per source")
	}

	src := sources[0]
	switch src.Type {
	case "volume":
		rc, err := tarVolume(ctx, e.docker, src.Name)
		if err != nil {
			return 0, fmt.Errorf("tar volume %s: %w", src.Name, err)
		}
		defer rc.Close()
		n, err := io.Copy(w, rc)
		total += n
		return total, err
	case "stack":
		// For stacks we tar the on-disk stack dir using the same helper
		// (mounted at /source) plus the volumes referenced by the
		// compose file.
		// MVP: just snapshot the stack dir; volumes can be backed up via
		// separate volume jobs. Document as a follow-up.
		dir, err := e.stacks.Dir(src.Name)
		if err != nil {
			return 0, fmt.Errorf("stack dir: %w", err)
		}
		// Walk dir, tar manually since we don't want a docker helper for
		// host paths.
		return tarHostDir(dir, w)
	case "system":
		return tarSystem(ctx, e.db, e.paths, w)
	default:
		return 0, fmt.Errorf("%w: %s", ErrUnknownSourceType, src.Type)
	}
}

func (e *Executor) runHooks(ctx context.Context, hooks []Hook) error {
	if len(hooks) == 0 || e.docker == nil {
		return nil
	}
	cli := e.docker.Raw()
	for _, h := range hooks {
		if h.Container == "" || len(h.Cmd) == 0 {
			continue
		}
		exec, err := cli.ContainerExecCreate(ctx, h.Container, dtypes.ExecConfig{
			Cmd: h.Cmd,
		})
		if err != nil {
			return fmt.Errorf("exec create %s: %w", h.Container, err)
		}
		if err := cli.ContainerExecStart(ctx, exec.ID, dtypes.ExecStartCheck{}); err != nil {
			return fmt.Errorf("exec start %s: %w", h.Container, err)
		}
		// Poll for completion (max 5 min).
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			info, err := cli.ContainerExecInspect(ctx, exec.ID)
			if err != nil {
				return err
			}
			if !info.Running {
				if info.ExitCode != 0 {
					return fmt.Errorf("exec %s exit %d", h.Container, info.ExitCode)
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	return nil
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

