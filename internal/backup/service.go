package backup

import (
	"context"
	"database/sql"
	"errors"
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
		exec:     newExecutor(st, db, dc, sm, sec, paths),
		docker:   dc,
		stacks:   sm,
		secrets:  sec,
		paths:    paths,
		cron:     cron.New(),
		entryIDs: make(map[int64]cron.EntryID),
	}
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
		return errors.New("stack restore not implemented yet — extract the .tar.gz manually")
	}
	return untarVolume(ctx, s.docker, destVolume, reader)
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
	if in.Schedule != "" {
		if _, err := cron.ParseStandard(in.Schedule); err != nil {
			return err
		}
	}
	return nil
}
