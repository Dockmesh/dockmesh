package backup

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
)

// DefaultSystemSchedule is the cron expression suggested for a daily
// system backup — 03:00 server-local time. Used when the operator
// explicitly opts in via SetDefaultJobEnabled or the future "Quick
// setup" UI button.
const DefaultSystemSchedule = "0 3 * * *"

// DefaultSystemRetentionDays is how many daily system backups we keep.
const DefaultSystemRetentionDays = 14

// EnsureDefaultJob creates the default daily system backup job if it
// doesn't already exist. **Not** called on boot anymore — only when
// the operator explicitly opts in (see SetDefaultJobEnabled). Earlier
// dockmesh versions called this from cmd/dockmesh/main.go on every
// start, which created a backup job the user never asked for and
// didn't necessarily want — see P.13.2.
func (s *Service) EnsureDefaultJob(ctx context.Context) error {
	if s.paths.DBPath == "" || s.paths.StacksRoot == "" || s.paths.DataDir == "" {
		slog.Info("backup default job skipped — system paths not configured")
		return nil
	}
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	// Any existing system-source job counts, so we don't end up with two
	// competing daily backups.
	if systemJob(jobs) != nil {
		return nil
	}

	in := JobInput{
		Name:       DefaultSystemJobName,
		TargetType: "local",
		TargetConfig: map[string]any{
			"path": filepath.Join(s.paths.DataDir, "backups"),
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
	slog.Info("backup default job created (operator opt-in)",
		"name", j.Name,
		"schedule", j.Schedule,
		"retention_days", j.RetentionDays,
		"encrypted", j.Encrypt,
	)
	return nil
}

// MigrateSystemJobPath gives a local system job that still has a relative
// path an absolute one. Earlier versions resolved it against the working
// directory, "/" under systemd. If archives of the job are already there the
// job keeps that directory, otherwise it moves to <data>/backups. It returns
// the old and new path when it changed the job.
func (s *Service) MigrateSystemJobPath(ctx context.Context) (from, to string, err error) {
	if s.paths.DataDir == "" {
		return "", "", nil
	}
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return "", "", err
	}
	j := systemJob(jobs)
	if j == nil || j.TargetType != "local" {
		return "", "", nil
	}
	var cfg map[string]any
	if b, err := json.Marshal(j.TargetConfig); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	from, _ = cfg["path"].(string)
	if from == "" {
		from = targets.DefaultLocalPath
	}
	if filepath.IsAbs(from) {
		return "", "", nil
	}
	old, err := filepath.Abs(from)
	if err != nil {
		return "", "", err
	}
	to = filepath.Join(s.paths.DataDir, "backups")
	if hasFiles(filepath.Join(old, "jobs", sanitize(j.Name))) {
		to = old
	}
	cfg["path"] = to
	in := JobInput{
		Name: j.Name, HostID: j.HostID, TargetType: j.TargetType, TargetConfig: cfg,
		Sources: j.Sources, Schedule: j.Schedule, RetentionCount: j.RetentionCount,
		RetentionDays: j.RetentionDays, Encrypt: j.Encrypt, PreHooks: j.PreHooks,
		PostHooks: j.PostHooks, Enabled: j.Enabled,
	}
	if err := s.store.updateJob(ctx, j.ID, in); err != nil {
		return "", "", err
	}
	slog.Info("system backup job path made absolute", "job", j.Name, "from", from, "to", to)
	return from, to, nil
}

func hasFiles(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipAll
		}
		if d.Type().IsRegular() {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// MarkAutoCreatedJobsForReview is a one-shot migration helper. On boot,
// if a job named DefaultSystemJobName exists and the review flag is
// not already set or cleared, mark it as needing operator review. The
// UI surfaces these as "auto-created — keep or disable?" so the
// operator can confirm they actually want the daily local backup that
// older dockmesh versions installed without asking. Idempotent: once
// the flag is cleared (Keep / Disable in the UI), it stays cleared.
//
// New installs never see this fire — there's no default job to flag.
func (s *Service) MarkAutoCreatedJobsForReview(ctx context.Context) error {
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	// Only the system job still carrying the stock name counts as
	// auto-created. Jobs that already carry the flag, or that the operator
	// has reviewed (review_acked is sticky), are left alone so this stays
	// idempotent across restarts.
	j := systemJob(jobs)
	if j == nil || j.Name != DefaultSystemJobName || j.NeedsReview || j.ReviewAcked {
		return nil
	}
	reason := "Auto-created by an earlier dockmesh version. Default behaviour changed in v0.3 — review and choose Keep or Disable. " +
		"Default jobs back up everything to a local directory, which can fill the disk and is not off-host. Move to an off-host target if you want to keep it."
	if err := s.store.markJobNeedsReview(ctx, j.ID, reason); err != nil {
		slog.Warn("flag legacy default job for review", "id", j.ID, "err", err)
		return nil
	}
	slog.Warn("backup default job flagged for review",
		"id", j.ID,
		"action_needed", "open the Backups page in the UI and choose Keep or Disable")
	return nil
}

// AcknowledgeReview clears the needs_review flag on a job. mode "keep"
// just clears the flag; mode "disable" also flips enabled=0 so the
// scheduler stops firing it. Used by the two review endpoints.
func (s *Service) AcknowledgeReview(ctx context.Context, id int64, mode string) error {
	j, err := s.store.getJob(ctx, id)
	if err != nil {
		return err
	}
	if mode == "disable" && j.Enabled {
		if _, err := s.SetJobEnabled(ctx, j.ID, false); err != nil {
			return err
		}
	}
	return s.store.clearJobReview(ctx, id)
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
	j := systemJob(jobs)
	if j == nil {
		return out, nil
	}
	jobID := j.ID
	out.Exists = true
	out.Enabled = j.Enabled
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
// backup job. If the job doesn't exist and the caller asks for enabled
// = true, the default job is created (operator opt-in path). Disable
// on a non-existent job is a no-op.
func (s *Service) SetDefaultJobEnabled(ctx context.Context, enabled bool) error {
	jobs, err := s.store.listJobs(ctx)
	if err != nil {
		return err
	}
	if j := systemJob(jobs); j != nil {
		_, err := s.SetJobEnabled(ctx, j.ID, enabled)
		return err
	}
	if enabled {
		return s.EnsureDefaultJob(ctx)
	}
	return nil
}

// systemJob returns the oldest job with a system source, or nil. The
// default job is found by its source, not its name, so another job can't
// take its place by being named like it.
func systemJob(jobs []Job) *Job {
	var out *Job
	for i := range jobs {
		if HasSystemSource(jobs[i].Sources) && (out == nil || jobs[i].ID < out.ID) {
			out = &jobs[i]
		}
	}
	return out
}
