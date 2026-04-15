package backup

import (
	"context"
	"log/slog"
)

// DefaultSystemSchedule is the cron expression for the auto-created
// daily system backup — 03:00 server-local time. Chosen to avoid the
// typical business-hours window while still running before most
// overnight batch jobs finish.
const DefaultSystemSchedule = "0 3 * * *"

// DefaultSystemRetentionDays is how many days worth of daily system
// backups we keep by default. 14 gives two weeks of restore points
// without ballooning disk usage for small installs.
const DefaultSystemRetentionDays = 14

// EnsureDefaultJob creates the default daily system backup job on
// first boot, if the user has no system-type backup job yet. Called
// from cmd/dockmesh after the scheduler starts so the newly-created
// job is picked up immediately.
//
// Idempotent: if a job with name DefaultSystemJobName already exists
// (regardless of enable state or user edits), this is a no-op. Users
// who delete the default job are not pestered to recreate it.
func (s *Service) EnsureDefaultJob(ctx context.Context) error {
	if s.paths.DBPath == "" || s.paths.StacksRoot == "" || s.paths.DataDir == "" {
		slog.Info("backup default job skipped — system paths not configured")
		return nil
	}
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Name == DefaultSystemJobName {
			return nil
		}
		// Also respect an existing user-created system-source job so we
		// don't end up with two competing daily backups.
		for _, src := range j.Sources {
			if src.Type == "system" {
				return nil
			}
		}
	}

	in := JobInput{
		Name:       DefaultSystemJobName,
		TargetType: "local",
		TargetConfig: map[string]any{
			"path": "./data/backups",
		},
		Sources: []Source{{
			Type: "system",
			Name: "dockmesh",
		}},
		Schedule:       DefaultSystemSchedule,
		RetentionCount: 0,
		RetentionDays:  DefaultSystemRetentionDays,
		Encrypt:        s.secrets != nil && s.secrets.Enabled(),
		Enabled:        true,
	}
	j, err := s.CreateJob(ctx, in)
	if err != nil {
		return err
	}
	slog.Info("backup default job created",
		"name", j.Name,
		"schedule", j.Schedule,
		"retention_days", j.RetentionDays,
		"encrypted", j.Encrypt,
	)
	return nil
}

// SystemStatus bundles the default-job state for the handler so we
// don't overload multi-return bools with ambiguous meanings.
type SystemStatus struct {
	Exists  bool
	Enabled bool
	Run     *Run // most recent run, or nil if none
}

// LastSystemRun looks up the default system backup job and its most
// recent run (if any). Used by the sidebar-pill handler.
func (s *Service) LastSystemRun(ctx context.Context) (SystemStatus, error) {
	var out SystemStatus
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return out, err
	}
	var jobID int64 = -1
	for _, j := range jobs {
		if j.Name == DefaultSystemJobName {
			jobID = j.ID
			out.Exists = true
			out.Enabled = j.Enabled
			break
		}
	}
	if !out.Exists {
		return out, nil
	}
	runs, err := s.store.listRuns(ctx, 100)
	if err != nil {
		return out, err
	}
	for i := range runs {
		if runs[i].JobID == jobID {
			r := runs[i]
			out.Run = &r
			break
		}
	}
	return out, nil
}

// SetDefaultJobEnabled flips the enabled flag on the default system
// backup job so the sidebar pill + settings toggle can turn automated
// backups on/off without exposing the full jobs CRUD.
func (s *Service) SetDefaultJobEnabled(ctx context.Context, enabled bool) error {
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Name != DefaultSystemJobName {
			continue
		}
		in := JobInput{
			Name:           j.Name,
			TargetType:     j.TargetType,
			TargetConfig:   j.TargetConfig,
			Sources:        j.Sources,
			Schedule:       j.Schedule,
			RetentionCount: j.RetentionCount,
			RetentionDays:  j.RetentionDays,
			Encrypt:        j.Encrypt,
			PreHooks:       j.PreHooks,
			PostHooks:      j.PostHooks,
			Enabled:        enabled,
		}
		_, err := s.UpdateJob(ctx, j.ID, in)
		return err
	}
	// No default job: if the caller wants to enable, create it.
	if enabled {
		return s.EnsureDefaultJob(ctx)
	}
	return nil
}

