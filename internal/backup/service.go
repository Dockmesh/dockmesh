package backup

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/docker"
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

	cron     *cron.Cron
	mu       sync.Mutex
	entryIDs map[int64]cron.EntryID
}

func NewService(db *sql.DB, dc *docker.Client, sm *stacks.Manager, sec *secrets.Service, paths SystemPaths) *Service {
	st := newStore(db)
	return &Service{
		store:    st,
		exec:     newExecutor(st, db, dc, nil, sm, sec, paths),
		docker:   dc,
		stacks:   sm,
		secrets:  sec,
		paths:    paths,
		cron:     cron.New(),
		entryIDs: make(map[int64]cron.EntryID),
	}
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

// Start loads enabled jobs and schedules them.
func (s *Service) Start(ctx context.Context) error {
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if !j.Enabled || j.Schedule == "" {
			continue
		}
		_ = s.schedule(&j)
	}
	s.cron.Start()
	return nil
}

func (s *Service) Stop() {
	if s.cron != nil {
		<-s.cron.Stop().Done()
	}
}

// schedule (re)registers a job's cron entry.
func (s *Service) schedule(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.entryIDs[j.ID]; ok {
		s.cron.Remove(id)
		delete(s.entryIDs, j.ID)
	}
	if !j.Enabled || j.Schedule == "" {
		return nil
	}
	jobID := j.ID
	id, err := s.cron.AddFunc(j.Schedule, func() {
		bg := context.Background()
		fresh, err := s.store.getJob(bg, jobID)
		if err != nil {
			return
		}
		_, _ = s.exec.Run(bg, fresh)
	})
	if err != nil {
		return err
	}
	s.entryIDs[j.ID] = id
	return nil
}

// -----------------------------------------------------------------------------
// public API used by handlers
// -----------------------------------------------------------------------------

func (s *Service) ListJobs(ctx context.Context) ([]Job, error) { return s.store.listJobs(ctx) }
func (s *Service) GetJob(ctx context.Context, id int64) (*Job, error) {
	return s.store.getJob(ctx, id)
}

func (s *Service) CreateJob(ctx context.Context, in JobInput) (*Job, error) {
	if err := validateJob(in); err != nil {
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
	s.mu.Lock()
	if cid, ok := s.entryIDs[id]; ok {
		s.cron.Remove(cid)
		delete(s.entryIDs, id)
	}
	s.mu.Unlock()
	return s.store.deleteJob(ctx, id)
}

// RunNow triggers a job manually and blocks until it finishes.
func (s *Service) RunNow(ctx context.Context, id int64) (*Run, error) {
	j, err := s.store.getJob(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.exec.Run(ctx, j)
}

func (s *Service) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	return s.store.listRuns(ctx, limit)
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
	if run.Status != "success" {
		return nil, errors.New("can only read from a successful run")
	}
	job, err := s.store.getJob(ctx, run.JobID)
	if err != nil {
		return nil, err
	}
	target, err := buildTarget(job.TargetType, job.TargetConfig)
	if err != nil {
		return nil, err
	}
	src, err := target.Read(ctx, run.TargetPath)
	if err != nil {
		return nil, err
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

// Restore fetches the run's archive from its target, decrypts if needed,
// and untars it into the named destination volume.
func (s *Service) Restore(ctx context.Context, runID int64, destVolume string) error {
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != "success" {
		return errors.New("can only restore from a successful run")
	}
	job, err := s.store.getJob(ctx, run.JobID)
	if err != nil {
		return err
	}
	target, err := buildTarget(job.TargetType, job.TargetConfig)
	if err != nil {
		return err
	}
	src, err := target.Read(ctx, run.TargetPath)
	if err != nil {
		return err
	}
	defer src.Close()

	var reader = src
	if run.Encrypted {
		dec, err := wrapDecrypt(src, s.secrets)
		if err != nil {
			return err
		}
		defer dec.Close()
		reader = dec
	}

	if len(run.Sources) > 0 && run.Sources[0].Type == "stack" {
		// destVolume is reused for stack restores: it carries the
		// target stack name (the volumes inside the archive are
		// named per their original project, so the caller picks
		// "restore as stack X"). When empty, fall back to the
		// source name from the run.
		stackName := destVolume
		if stackName == "" && len(run.Sources) > 0 {
			stackName = run.Sources[0].Name
		}
		if stackName == "" {
			return errors.New("stack restore: target stack name is required")
		}
		return s.restoreStack(ctx, stackName, reader)
	}
	return untarVolume(ctx, s.docker, destVolume, reader)
}

// StackRestoreReport tells the caller what actually happened on a stack
// restore — files extracted, volumes restored, anything skipped — so
// the UI can show a list rather than a flat success/fail.
type StackRestoreReport struct {
	StackName       string   `json:"stack_name"`
	FilesRestored   []string `json:"files_restored"`
	VolumesRestored []string `json:"volumes_restored"`
	Warnings        []string `json:"warnings,omitempty"`
}

// RestoreStack is the explicit entry point for a stack-typed run. Same
// shape as Restore but always returns the structured report. Used by
// the new POST /backups/runs/{id}/restore-stack endpoint and by the
// stack-detail recovery panel ("Restore from last backup" card).
func (s *Service) RestoreStack(ctx context.Context, runID int64, targetStack string) (*StackRestoreReport, error) {
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != "success" {
		return nil, errors.New("can only restore from a successful run")
	}
	if len(run.Sources) == 0 || run.Sources[0].Type != "stack" {
		return nil, errors.New("run is not a stack backup")
	}
	if targetStack == "" {
		targetStack = run.Sources[0].Name
	}
	job, err := s.store.getJob(ctx, run.JobID)
	if err != nil {
		return nil, err
	}
	target, err := buildTarget(job.TargetType, job.TargetConfig)
	if err != nil {
		return nil, err
	}
	src, err := target.Read(ctx, run.TargetPath)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	var reader io.Reader = src
	if run.Encrypted {
		dec, err := wrapDecrypt(src, s.secrets)
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		reader = dec
	}

	report := &StackRestoreReport{StackName: targetStack}
	if err := s.restoreStackInto(ctx, targetStack, reader, report); err != nil {
		return report, err
	}
	return report, nil
}

// restoreStack is the legacy single-error wrapper kept around for the
// old Restore() entry point. New code should call RestoreStack.
func (s *Service) restoreStack(ctx context.Context, stackName string, reader io.Reader) error {
	report := &StackRestoreReport{StackName: stackName}
	return s.restoreStackInto(ctx, stackName, reader, report)
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
	if in.TargetType == "" {
		return errors.New("target_type required")
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
