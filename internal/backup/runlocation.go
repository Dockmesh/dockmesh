package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/operations"
)

// RunLocation is where a run wrote its archive: the target and the
// target's settings at that time, without credentials.
type RunLocation struct {
	TargetID int64          `json:"-"`
	Type     string         `json:"type"`
	Config   map[string]any `json:"config"`
	// Inferred marks a location stamped on a run from before locations
	// were recorded, taken from the target just before it changed. It is
	// the best guess, not a record.
	Inferred  bool       `json:"inferred,omitempty"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
}

func (l *RunLocation) describe() string {
	return targets.DescribeLocation(l.Type, l.Config)
}

// storedLocation is where a saved target points now.
func storedLocation(t *targets.StoredTarget) *RunLocation {
	return &RunLocation{TargetID: t.ID, Type: t.Type, Config: targets.Location(t.Type, t.Config)}
}

func sameRunLocation(a, b *RunLocation) bool {
	return a != nil && b != nil && a.TargetID == b.TargetID && a.Type == b.Type &&
		targets.SameLocation(a.Type, a.Config, b.Config)
}

// ErrArchiveMissing means a run's archive is not where Dockmesh looked.
var ErrArchiveMissing = errors.New("archive not found")

// ArchiveMissingError says where a run's archive was expected and why it
// could not be read there.
type ArchiveMissingError struct{ msg string }

func (e *ArchiveMissingError) Error() string        { return e.msg }
func (e *ArchiveMissingError) Is(target error) bool { return target == ErrArchiveMissing }

func missing(format string, args ...any) error {
	return &ArchiveMissingError{msg: fmt.Sprintf(format, args...)}
}

const stampTime = "2006-01-02 15:04 MST"

// openRunArchive opens the archive of run as stored on its target. Runs
// that recorded a location read from there with the current credentials
// of their target; runs from before that read from the job's target.
func (s *Service) openRunArchive(ctx context.Context, run *Run, job *Job) (io.ReadCloser, error) {
	cands, err := runCandidates(ctx, s.store, s.notices, run, job)
	if err != nil {
		return nil, err
	}
	var notFound []string
	for _, c := range cands {
		rc, err := c.tgt.Read(ctx, run.TargetPath)
		if err == nil {
			return rc, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		notFound = append(notFound, c.desc)
	}
	return nil, archiveNotFound(ctx, s.store, run, job, notFound)
}

type candidate struct {
	tgt  targets.Target
	desc string
}

// runCandidates lists the places a run's archive may be at, most likely
// first: the recorded location, then (for runs without a recorded one)
// the job's current target.
func runCandidates(ctx context.Context, st *store, n *notices.Service, run *Run, job *Job) ([]candidate, error) {
	var out []candidate
	if loc := run.Location; loc != nil && (loc.TargetID > 0 || job.TargetID == 0) {
		t, err := targetAt(ctx, st, n, run, job, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate{t, loc.describe()})
		if !loc.Inferred {
			return out, nil
		}
	}
	t, cur, err := jobTargetAt(ctx, st, n, job)
	if err != nil {
		if len(out) > 0 {
			return out, nil
		}
		return nil, err
	}
	if run.Location == nil || !targets.SameLocation(cur.Type, cur.Config, run.Location.Config) || cur.Type != run.Location.Type {
		out = append(out, candidate{t, cur.describe()})
	}
	return out, nil
}

// targetAt builds the target a run recorded. The credentials are those
// the target has now; a target that changed type or was deleted can't be
// opened, and the error says where the archive is.
func targetAt(ctx context.Context, st *store, n *notices.Service, run *Run, job *Job, loc *RunLocation) (targets.Target, error) {
	if loc.TargetID == 0 {
		return jobInlineTargetAt(ctx, st, n, job, loc)
	}
	ts := st.targets()
	stored, err := ts.Get(ctx, loc.TargetID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, missing("run %d was written to backup target #%d, which was deleted; its archive %s is still at %s, but can't be read without a target holding the credentials",
			run.ID, loc.TargetID, run.TargetPath, loc.describe())
	}
	if err != nil {
		return nil, err
	}
	if stored.Type != loc.Type {
		return nil, missing("run %d was written to %s; backup target %q is now of type %s, so the archive %s can't be read through it",
			run.ID, loc.describe(), stored.Name, stored.Type, run.TargetPath)
	}
	operations.FromContext(ctx).AddSecrets(targetSecretValues(stored.Type, stored.Config)...)
	return atRecorded(run, loc, fmt.Sprintf("backup target %q", stored.Name), stored.Config, func() (targets.Target, error) {
		return ts.BuildChecked(ctx, stored, n)
	})
}

// jobInlineTargetAt is targetAt for a job that still keeps its target
// config inline.
func jobInlineTargetAt(ctx context.Context, st *store, n *notices.Service, job *Job, loc *RunLocation) (targets.Target, error) {
	if job.TargetType != loc.Type {
		return nil, missing("the archive was written to %s, a target the job no longer uses", loc.describe())
	}
	if job.CredentialsError != "" {
		return nil, fmt.Errorf("backup job %q: %s", job.Name, job.CredentialsError)
	}
	operations.FromContext(ctx).AddSecrets(targetSecretValues(job.TargetType, job.TargetConfig)...)
	return atRecorded(nil, loc, fmt.Sprintf("backup job %q", job.Name), job.TargetConfig, func() (targets.Target, error) {
		t, err := buildTarget(job.TargetType, job.TargetConfig)
		if err != nil {
			return nil, err
		}
		jobTOFU(st, n, job).Attach(ctx, t)
		return t, nil
	})
}

// atRecorded opens loc with the config cur of the target (or inline job)
// named owner that the run wrote through; checked builds cur itself, with
// its host-key check. Credentials are only used for the server and
// account they were entered for: once owner points at another server or
// user, a recorded location there is refused, so editing a target can't
// make Dockmesh send later-entered credentials to a server of the
// editor's choosing.
func atRecorded(run *Run, loc *RunLocation, owner string, cur any, checked func() (targets.Target, error)) (targets.Target, error) {
	now := targets.Location(loc.Type, cur)
	if !targets.SameAccount(loc.Config, now) {
		what := "the archive"
		if run != nil {
			what = fmt.Sprintf("the archive %s of run %d", run.TargetPath, run.ID)
		}
		return nil, missing("%s was written to %s; %s now points at another server or account, and its credentials are only used where they were entered. Point it back at that server to read %s",
			what, loc.describe(), owner, what)
	}
	recordedKey, _ := loc.Config["host_key"].(string)
	currentKey, _ := now["host_key"].(string)
	if targets.SameLocation(loc.Type, loc.Config, now) && (recordedKey == "" || currentKey != "") {
		t, err := checked()
		if err != nil {
			return nil, err
		}
		return wrapRunTarget(t), nil
	}
	cfg := targets.AtLocation(loc.Type, loc.Config, cur)
	if k, _ := cfg["host_key"].(string); loc.Type == "sftp" && k == "" {
		return nil, fmt.Errorf("no host key was recorded for %s, so Dockmesh won't connect to it to read the archive", loc.describe())
	}
	built, err := targets.Build(loc.Type, cfg)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", loc.describe(), err)
	}
	return wrapRunTarget(built), nil
}

// archiveNotFound explains a run whose archive is in none of the places
// tried, naming the most likely one.
func archiveNotFound(ctx context.Context, st *store, run *Run, job *Job, tried []string) error {
	where := strings.Join(tried, " or ")
	loc := run.Location
	switch {
	case loc != nil && loc.Inferred && loc.ChangedAt != nil:
		return missing("archive %s of run %d was not found at %s: it was most likely written under %s before the target changed on %s, and was moved or deleted since",
			run.TargetPath, run.ID, where, loc.describe(), loc.ChangedAt.Format(stampTime))
	case loc != nil:
		return missing("archive %s of run %d is no longer at %s, where it was written; it was moved or deleted outside Dockmesh",
			run.TargetPath, run.ID, where)
	}
	msg := fmt.Sprintf("archive %s of run %d was not found at %s", run.TargetPath, run.ID, where)
	if job.TargetID > 0 {
		if t, err := st.targets().Get(ctx, job.TargetID); err == nil && t.UpdatedAt.After(run.StartedAt) {
			msg += fmt.Sprintf(": the run was taken on %s, before Dockmesh recorded where each run writes, and backup target %q was changed on %s, so the archive is most likely still where that target pointed before the change",
				run.StartedAt.Format(stampTime), t.Name, t.UpdatedAt.Format(stampTime))
			return missing("%s", msg)
		}
	}
	if job.UpdatedAt.After(run.StartedAt) {
		msg += fmt.Sprintf(": the run was taken on %s, before Dockmesh recorded where each run writes, and the job was changed on %s, so the archive is most likely on the target the job used before",
			run.StartedAt.Format(stampTime), job.UpdatedAt.Format(stampTime))
	}
	return missing("%s", msg)
}

func marshalLocation(loc *RunLocation) (sql.NullInt64, sql.NullString) {
	if loc == nil {
		return sql.NullInt64{}, sql.NullString{}
	}
	raw, err := json.Marshal(loc)
	if err != nil {
		return sql.NullInt64{}, sql.NullString{}
	}
	return sql.NullInt64{Int64: loc.TargetID, Valid: loc.TargetID > 0}, sql.NullString{String: string(raw), Valid: true}
}

func unmarshalLocation(targetID sql.NullInt64, raw sql.NullString) *RunLocation {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var loc RunLocation
	if err := json.Unmarshal([]byte(raw.String), &loc); err != nil {
		return nil
	}
	loc.TargetID = targetID.Int64
	return &loc
}

// stampLegacyRuns records loc as the inferred location of the runs that
// have none recorded, of the jobs on target targetID or of job jobID
// (0 matches all). It runs right before the place loc describes changes,
// which is the last moment it is known.
func (s *store) stampLegacyRuns(ctx context.Context, loc *RunLocation, targetID, jobID int64) error {
	now := time.Now().UTC()
	stamp := *loc
	stamp.Inferred, stamp.ChangedAt = true, &now
	tid, raw := marshalLocation(&stamp)
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_runs SET target_id = ?, target_location = ?
		WHERE target_location IS NULL AND pruned_at IS NULL AND COALESCE(target_path, '') != ''
		  AND (? = 0 OR job_id IN (SELECT id FROM backup_jobs WHERE target_id = ?))
		  AND (? = 0 OR job_id = ?)`,
		tid, raw, targetID, targetID, jobID, jobID)
	return err
}

// countElsewhere counts the restorable runs recorded on target targetID
// or of job jobID (0 matches all) whose archive is not at want.
func (s *store) countElsewhere(ctx context.Context, want *RunLocation, targetID, jobID int64) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target_id, target_location FROM backup_runs
		WHERE status IN ('success', 'success_with_warnings') AND pruned_at IS NULL
		  AND COALESCE(target_path, '') != ''
		  AND (? = 0 OR target_id = ?) AND (? = 0 OR job_id = ?)`,
		targetID, targetID, jobID, jobID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var tid sql.NullInt64
		var raw sql.NullString
		if err := rows.Scan(&tid, &raw); err != nil {
			return 0, err
		}
		if !sameRunLocation(unmarshalLocation(tid, raw), want) {
			n++
		}
	}
	return n, rows.Err()
}

// PrepareTargetChange is called before target id is saved as newType with
// newCfg. When that moves the target, runs written to it that recorded no
// location get its current one, and the result is the number of kept
// archives that stay at an earlier location of the target.
func (s *Service) PrepareTargetChange(ctx context.Context, id int64, newType string, newCfg any) (int, error) {
	stored, err := s.store.targets().Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	prev := storedLocation(stored)
	next := &RunLocation{TargetID: id, Type: newType, Config: targets.Location(newType, newCfg)}
	if sameRunLocation(prev, next) {
		return 0, nil
	}
	if err := s.store.stampLegacyRuns(ctx, prev, id, 0); err != nil {
		return 0, err
	}
	return s.store.countElsewhere(ctx, next, id, 0)
}

// prepareJobSwitch stamps the job's runs without a recorded location with
// the location of the target the job is leaving.
func (s *Service) prepareJobSwitch(ctx context.Context, prev *Job, nextTargetID int64) error {
	if prev.TargetID == 0 || prev.TargetID == nextTargetID {
		return nil
	}
	stored, err := s.store.targets().Get(ctx, prev.TargetID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.store.stampLegacyRuns(ctx, storedLocation(stored), 0, prev.ID)
}

// runsElsewhere counts the job's kept archives that are not on its target.
func (s *Service) runsElsewhere(ctx context.Context, job *Job) (int, error) {
	if job.TargetID == 0 {
		return 0, nil
	}
	stored, err := s.store.targets().Get(ctx, job.TargetID)
	if err != nil {
		return 0, err
	}
	return s.store.countElsewhere(ctx, storedLocation(stored), 0, job.ID)
}
