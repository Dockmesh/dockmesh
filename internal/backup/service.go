package backup

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/robfig/cron/v3"
)

// Service is the public facade for the backup subsystem. It owns the
// store, executor, and a cron scheduler that fires Run() on jobs whose
// schedule expression matches.
type Service struct {
	store   *store
	exec    *Executor
	docker  *docker.Client
	stacks  *stacks.Manager
	secrets *secrets.Service
	paths   SystemPaths

	mu       sync.Mutex
	cron     *cron.Cron
	started  bool
	tzName   string // backups.timezone; empty = server time zone
	entryIDs map[int64]cron.EntryID
	running  map[int64]bool

	// runCtx is cancelled on Stop; background runs are tracked in runs.
	runCtx    context.Context
	cancelRun context.CancelFunc
	runs      sync.WaitGroup

	notices *notices.Service
}

// SetNotices wires action-required notices (SFTP host keys).
func (s *Service) SetNotices(n *notices.Service) {
	s.notices = n
	s.exec.notices = n
}

func NewService(db *sql.DB, dc *docker.Client, sm *stacks.Manager, sec *secrets.Service, paths SystemPaths) *Service {
	st := newStore(db)
	runCtx, cancel := context.WithCancel(context.Background())
	return &Service{
		store:     st,
		exec:      newExecutor(st, db, dc, nil, sm, sec, paths),
		docker:    dc,
		stacks:    sm,
		secrets:   sec,
		paths:     paths,
		cron:      newCron(time.Local),
		entryIDs:  make(map[int64]cron.EntryID),
		running:   make(map[int64]bool),
		runCtx:    runCtx,
		cancelRun: cancel,
	}
}

// SetNotifier wires in the bell-icon notification center so each
// finished run emits a success/failure notification. Optional — nil
// means runs only land in the runs table + journald, no bell update.
func (s *Service) SetNotifier(n *notifications.Service) {
	s.exec.notifs = n
}

// SetHostResolver wires in the host registry post-construction so the
// executor can route per-job to local vs remote agents. Called by
// main.go after both backup and host registry are constructed (they
// can't be created in a single line — host registry needs the DB, backup
// registers restorers on host etc.). Optional: if never called, backup
// falls back to local-only behavior with a clear error on non-local
// host_id values.
func (s *Service) SetHostResolver(hr hostResolver) {
	s.exec.hosts = hr
}

// -----------------------------------------------------------------------------
// public API used by handlers
// -----------------------------------------------------------------------------

func (s *Service) ListJobs(ctx context.Context) ([]Job, error) {
	jobs, err := s.store.listJobs(ctx)
	for i := range jobs {
		jobs[i].NextRunAt = s.nextRun(jobs[i].ID)
	}
	return jobs, err
}

// UnencryptableJobs lists enabled jobs set to encrypt while secrets
// encryption is off; their runs fail instead of writing plaintext.
func (s *Service) UnencryptableJobs(ctx context.Context) ([]string, error) {
	if s.canEncrypt() {
		return nil, nil
	}
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, j := range jobs {
		if j.Enabled && j.Encrypt {
			out = append(out, j.Name)
		}
	}
	return out, nil
}

func (s *Service) GetJob(ctx context.Context, id int64) (*Job, error) {
	j, err := s.store.getJob(ctx, id)
	if err == nil {
		j.NextRunAt = s.nextRun(id)
	}
	return j, err
}

func (s *Service) canEncrypt() bool { return canEncrypt(s.secrets) }

func canEncrypt(sec *secrets.Service) bool { return sec != nil && sec.Enabled() }

// checkJobInput applies the save-time rules that depend on the server:
// encryption needs secrets (a disabled job may keep the flag, so it can
// still be switched off). Local paths are checked when the target is built.
func (s *Service) checkJobInput(in JobInput) error {
	if in.Encrypt && in.Enabled && !s.canEncrypt() {
		return ErrEncryptUnavailable
	}
	return nil
}

func (s *Service) CreateJob(ctx context.Context, in JobInput) (*Job, error) {
	if err := validateJob(in); err != nil {
		return nil, err
	}
	if err := s.checkJobInput(in); err != nil {
		return nil, err
	}
	if err := s.attachTarget(ctx, &in); err != nil {
		return nil, err
	}
	id, err := s.store.createJob(ctx, in)
	if err != nil {
		return nil, err
	}
	j, err := s.store.getJob(ctx, id)
	if err != nil {
		return nil, err
	}
	_ = s.schedule(j)
	return j, nil
}

func (s *Service) UpdateJob(ctx context.Context, id int64, in JobInput) (*Job, error) {
	if err := validateJob(in); err != nil {
		return nil, err
	}
	if err := s.checkJobInput(in); err != nil {
		return nil, err
	}
	if _, err := s.store.getJob(ctx, id); err != nil {
		return nil, err
	}
	if err := s.attachTarget(ctx, &in); err != nil {
		return nil, err
	}
	if err := s.store.updateJob(ctx, id, in); err != nil {
		return nil, err
	}
	j, err := s.store.getJob(ctx, id)
	if err != nil {
		return nil, err
	}
	_ = s.schedule(j)
	return j, nil
}

func (s *Service) DeleteJob(ctx context.Context, id int64) error {
	s.unschedule(id)
	return s.store.deleteJob(ctx, id)
}

// RunNow runs a job and blocks until it finishes.
func (s *Service) RunNow(ctx context.Context, id int64) (*Run, error) {
	j, err := s.store.getJob(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.claim(id) {
		return nil, ErrJobRunning
	}
	defer s.release(id)
	return s.exec.Run(ctx, j)
}

// StartRun starts a job in the background and returns its run record
// right away, so the run doesn't depend on the HTTP request that asked
// for it. Poll GetRun for the outcome.
func (s *Service) StartRun(ctx context.Context, id int64) (*Run, error) {
	j, err := s.store.getJob(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.claim(id) {
		return nil, ErrJobRunning
	}
	runID, err := s.exec.begin(ctx, j)
	if err != nil {
		s.release(id)
		return nil, err
	}
	s.runs.Add(1)
	go func() {
		defer s.runs.Done()
		defer s.release(id)
		_, _ = s.exec.complete(s.runCtx, j, runID)
	}()
	return s.store.getRun(ctx, runID)
}

func (s *Service) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	return s.store.listRuns(ctx, limit)
}

// GetRun returns a single run by id. Used by the run-detail drawer and
// the archive-download endpoint; the latter needs to assert the run
// succeeded (and exists) before streaming bytes.
func (s *Service) GetRun(ctx context.Context, id int64) (*Run, error) {
	return s.store.getRun(ctx, id)
}

// RunLog returns the executor's captured log lines for one run in
// chronological order. Includes phase narration, hook stdout/stderr,
// and error lines. Used by the run-detail page's Log section.
func (s *Service) RunLog(ctx context.Context, id int64) ([]RunLogEntry, error) {
	return s.store.listRunLogs(ctx, id)
}

// RunSourceType returns the source type ("system" | "stack" | "volume")
// of the run's first source. Used by the verify handler to dispatch
// to the right verifier without reading the archive twice.
func (s *Service) RunSourceType(ctx context.Context, runID int64) (string, error) {
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if len(run.Sources) == 0 {
		return "", errors.New("run has no sources")
	}
	return run.Sources[0].Type, nil
}

// ReadRun opens the archive for a saved run and returns a reader.
// Caller must Close() it. If the run was encrypted, the reader is
// wrapped so the consumer sees plaintext. Used by the verify-by-run
// endpoint so operators don't have to download archives manually.
func (s *Service) ReadRun(ctx context.Context, runID int64) (io.ReadCloser, error) {
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	return s.openRun(ctx, run, nil)
}

// openRun opens a run's archive as plaintext. rawTap, when set, also
// receives the bytes exactly as they are stored on the target.
func (s *Service) openRun(ctx context.Context, run *Run, rawTap io.Writer) (io.ReadCloser, error) {
	if !run.Restorable() {
		return nil, ErrNotRestorable
	}
	job, err := s.store.getJobForRun(ctx, run.JobID)
	if err != nil {
		return nil, err
	}
	target, err := jobTarget(ctx, s.store, s.notices, job)
	if err != nil {
		return nil, err
	}
	src, err := target.Read(ctx, run.TargetPath)
	if err != nil {
		return nil, err
	}
	if rawTap != nil {
		src = &tappedReader{Reader: io.TeeReader(src, rawTap), Closer: src}
	}
	if !run.Encrypted {
		return src, nil
	}
	dec, err := wrapDecrypt(src, s.secrets)
	if err != nil {
		_ = src.Close()
		return nil, err
	}
	return &readRunCloser{r: dec, underlying: src}, nil
}

type tappedReader struct {
	io.Reader
	io.Closer
}

// readRunCloser closes the decrypt wrapper then the underlying source.
type readRunCloser struct {
	r          io.ReadCloser
	underlying io.ReadCloser
}

func (c *readRunCloser) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *readRunCloser) Close() error {
	err1 := c.r.Close()
	err2 := c.underlying.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func buildTarget(typ string, cfg any) (targets.Target, error) {
	switch typ {
	case "local":
		return targets.NewLocal(cfg)
	case "s3":
		return targets.NewS3(cfg)
	case "sftp":
		return targets.NewSFTP(cfg)
	case "smb":
		return targets.NewSMB(cfg)
	case "webdav":
		return targets.NewWebDAV(cfg)
	}
	return nil, ErrUnknownTargetType
}

func validateJob(in JobInput) error {
	if in.Name == "" {
		return errors.New("name required")
	}
	if in.TargetID == 0 && in.TargetType == "" {
		return errors.New("target_id or target_type required")
	}
	if len(in.Sources) == 0 {
		return errors.New("at least one source required")
	}
	if len(in.Sources) > 1 {
		// P.13.5: rather than letting jobs land in the DB with multiple
		// sources and silently truncating to the first one at run time
		// (the old behaviour, which produced incomplete backups under
		// the radar), reject up front. Operators who need to back up
		// several things create one job per source.
		return errors.New("exactly one source per job — create one job per stack/volume/system you want to back up")
	}
	if in.Schedule != "" {
		if _, err := cron.ParseStandard(in.Schedule); err != nil {
			return err
		}
	}
	return nil
}
