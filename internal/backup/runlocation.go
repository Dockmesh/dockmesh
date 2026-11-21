package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
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

// ErrArchiveGone means the places a run's archive was looked for were
// reachable and the file was in none of them. It also matches
// ErrArchiveMissing.
var ErrArchiveGone = errors.New("archive missing")

// ArchiveMissingError says where a run's archive was expected and why it
// could not be read there.
type ArchiveMissingError struct {
	msg  string
	gone bool
}

func (e *ArchiveMissingError) Error() string { return e.msg }
func (e *ArchiveMissingError) Is(target error) bool {
	return target == ErrArchiveMissing || (e.gone && target == ErrArchiveGone)
}

func missing(format string, args ...any) error {
	return &ArchiveMissingError{msg: fmt.Sprintf(format, args...)}
}

func gone(format string, args ...any) error {
	return &ArchiveMissingError{msg: fmt.Sprintf(format, args...), gone: true}
}

// ErrRunJobGone means a run's job row no longer exists and nothing the
// run recorded leads to its archive.
var ErrRunJobGone = errors.New("the run's job was deleted")

// RunJobGoneError says why the archive of a run without a job can't be
// opened and what the operator can do instead.
type RunJobGoneError struct{ msg string }

func (e *RunJobGoneError) Error() string        { return e.msg }
func (e *RunJobGoneError) Is(target error) bool { return target == ErrRunJobGone }

func jobGone(format string, args ...any) error {
	return &RunJobGoneError{msg: fmt.Sprintf(format, args...)}
}

const stampTime = "2006-01-02 15:04 MST"

// openRunArchive opens the archive of run as stored on its target and
// records whether it was found: a run whose archive is gone is marked
// archive missing, one whose archive turns up again is unmarked.
func (s *Service) openRunArchive(ctx context.Context, run *Run, job *Job) (io.ReadCloser, error) {
	rc, err := s.locateRunArchive(ctx, run, job)
	switch {
	case err == nil:
		if cerr := s.store.clearArchiveMissing(ctx, run.ID); cerr != nil {
			slog.Warn("backup: clear archive missing", "run", run.ID, "err", cerr)
		}
	case errors.Is(err, ErrArchiveGone):
		if merr := s.store.markArchiveMissing(ctx, run.ID, err.Error()); merr != nil {
			slog.Warn("backup: mark archive missing", "run", run.ID, "err", merr)
		}
	}
	return rc, err
}

// locateRunArchive opens the archive of run as stored on its target. Runs
// that recorded a location read from there with the current credentials
// of their target; runs from before that read from the job's target.
func (s *Service) locateRunArchive(ctx context.Context, run *Run, job *Job) (io.ReadCloser, error) {
	if job == nil {
		return s.openJoblessArchive(ctx, run)
	}
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
	certain := !guessed(run)
	if guessed(run) && run.SHA256 != "" {
		rc, _, unreachable, err := searchSavedTargets(ctx, s.store, s.notices, run)
		if rc != nil || err != nil {
			return rc, err
		}
		certain = !unreachable
	}
	return nil, archiveNotFound(ctx, s.store, run, job, notFound, certain)
}

// guessed reports whether nothing certain is known about where run wrote
// its archive.
func guessed(run *Run) bool {
	return run.Location == nil || run.Location.Inferred
}

// openJoblessArchive opens the archive of a run whose job row is gone,
// which only happens for jobs deleted outright by older versions. Such
// runs recorded nothing but the archive's path and hash, so every saved
// target is tried and the hash decides whether the file found is the
// run's archive.
func (s *Service) openJoblessArchive(ctx context.Context, run *Run) (io.ReadCloser, error) {
	if loc := run.Location; loc != nil && loc.TargetID > 0 {
		t, err := targetAt(ctx, s.store, s.notices, run, nil, loc)
		switch {
		case err == nil:
			rc, err := t.Read(ctx, run.TargetPath)
			if err == nil {
				return rc, nil
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			if !loc.Inferred {
				return nil, gone("archive %s of run %d is no longer at %s, where it was written; it was moved or deleted outside Dockmesh",
					run.TargetPath, run.ID, loc.describe())
			}
		case !loc.Inferred:
			return nil, err
		}
	}
	if run.SHA256 == "" {
		return nil, jobGone("the job of run %d was deleted and the run recorded neither its target nor a hash of its archive %s, so Dockmesh can't tell which file is its archive; copy the archive from the target the job wrote to and restore it by hand",
			run.ID, run.TargetPath)
	}
	rc, tried, _, err := searchSavedTargets(ctx, s.store, s.notices, run)
	if rc != nil || err != nil {
		return rc, err
	}
	where := "there are no saved backup targets"
	if len(tried) > 0 {
		where = "it is on none of the saved backup targets " + strings.Join(tried, ", ")
	}
	return nil, jobGone("the job of run %d was deleted and the run didn't record its target; %s. Add the target the job wrote to again as a saved target, or copy the archive %s from it and restore it by hand",
		run.ID, where, run.TargetPath)
}

// searchSavedTargets looks for run's archive at its path on every saved
// target and returns the first file with the run's hash and size, so it
// is the run's own archive wherever it is found. tried describes the
// targets without it; unreachable is set when one of them couldn't be
// read, so the archive may still be there.
func searchSavedTargets(ctx context.Context, st *store, n *notices.Service, run *Run) (rc io.ReadCloser, tried []string, unreachable bool, err error) {
	ts := st.targets()
	saved, err := ts.List(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	tried = make([]string, 0, len(saved))
	for i := range saved {
		t := &saved[i]
		desc := fmt.Sprintf("%q", t.Name)
		operations.FromContext(ctx).AddSecrets(targetSecretValues(t.Type, t.Config)...)
		built, err := ts.BuildChecked(ctx, t, n)
		if err == nil {
			tgt := wrapRunTarget(built)
			var match bool
			if match, err = holdsArchive(ctx, tgt, run); err == nil && match {
				var rc io.ReadCloser
				if rc, err = tgt.Read(ctx, run.TargetPath); err == nil {
					return &hashCheckedReader{rc: rc, h: sha256.New(), want: run.SHA256, where: "backup target " + desc}, nil, false, nil
				}
			} else if err == nil {
				tried = append(tried, desc+" (file does not match)")
				continue
			}
		}
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("backup: look for the archive of a run on the saved targets", "run", run.ID, "target", t.Name, "err", err)
			desc += " (unreachable)"
			unreachable = true
		}
		tried = append(tried, desc)
	}
	return nil, tried, unreachable, nil
}

// holdsArchive reports whether the file at the run's path on t has the
// run's hash and size. The whole file is read: another job of the same
// name may have written the same path to another target.
func holdsArchive(ctx context.Context, t targets.Target, run *Run) (bool, error) {
	rc, err := t.Read(ctx, run.TargetPath)
	if err != nil {
		return false, err
	}
	defer rc.Close()
	h := sha256.New()
	var src io.Reader = ctxReader{ctx: ctx, r: rc}
	if run.SizeBytes > 0 {
		src = io.LimitReader(src, run.SizeBytes+1)
	}
	n, err := io.Copy(h, src)
	if err != nil {
		return false, err
	}
	sizeOK := run.SizeBytes <= 0 || n == run.SizeBytes
	return sizeOK && strings.EqualFold(hex.EncodeToString(h.Sum(nil)), run.SHA256), nil
}

// hashCheckedReader fails the read that reaches the end of an archive
// whose bytes don't hash to want, in case the file changed after
// holdsArchive read it.
type hashCheckedReader struct {
	rc    io.ReadCloser
	h     hash.Hash
	want  string
	where string
}

func (r *hashCheckedReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	r.h.Write(p[:n])
	if errors.Is(err, io.EOF) {
		if got := hex.EncodeToString(r.h.Sum(nil)); !strings.EqualFold(got, r.want) {
			return n, fmt.Errorf("%w: the file found on %s has sha256 %s, the run recorded %s", ErrArchiveMismatch, r.where, got, r.want)
		}
	}
	return n, err
}

func (r *hashCheckedReader) Close() error { return r.rc.Close() }

func (r *hashCheckedReader) Break() error { return targets.Break(r.rc) }

type candidate struct {
	tgt  targets.Target
	desc string
}

// runCandidates lists the places a run's archive may be at, most likely
// first: the recorded location, then (for runs without a recorded one)
// the job's current target.
func runCandidates(ctx context.Context, st *store, n *notices.Service, run *Run, job *Job) ([]candidate, error) {
	var out []candidate
	var refused error
	if loc := run.Location; loc != nil && (loc.TargetID > 0 || job.TargetID == 0) {
		t, err := targetAt(ctx, st, n, run, job, loc)
		switch {
		case err == nil:
			out = append(out, candidate{t, loc.describe()})
		case !loc.Inferred || !errors.Is(err, ErrArchiveMissing):
			return nil, err
		default:
			// An inferred location is a guess; the job's target may hold
			// the archive after all.
			refused = err
		}
		if !loc.Inferred {
			return out, nil
		}
	}
	t, cur, err := jobTargetAt(ctx, st, n, job)
	switch {
	case err != nil && len(out) > 0:
		return out, nil
	case err != nil && refused != nil:
		return nil, refused
	case err != nil:
		return nil, err
	}
	if !sameRunLocation(cur, run.Location) {
		out = append(out, candidate{t, cur.describe()})
	}
	if len(out) == 0 && refused != nil {
		return nil, refused
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
		return nil, missing("no host key was recorded for %s, so Dockmesh won't connect to it to read the archive", loc.describe())
	}
	built, err := targets.Build(loc.Type, cfg)
	if err != nil {
		return nil, fmt.Errorf("open the place the run wrote to: %w", err)
	}
	return wrapRunTarget(built), nil
}

// archiveNotFound explains a run whose archive is in none of the places
// tried, naming the most likely one. certain says every place the archive
// could be was reachable; it counts as gone only then, and only when its
// target wasn't moved since the run, since an earlier location of a
// target is not searched.
func archiveNotFound(ctx context.Context, st *store, run *Run, job *Job, tried []string, certain bool) error {
	where := strings.Join(tried, " or ")
	loc := run.Location
	switch {
	case loc != nil && loc.Inferred && loc.ChangedAt != nil:
		return missing("archive %s of run %d was not found at %s: it was most likely written under %s before the target changed on %s, and was moved or deleted since",
			run.TargetPath, run.ID, where, loc.describe(), loc.ChangedAt.Format(stampTime))
	case loc != nil && !loc.Inferred:
		return gone("archive %s of run %d is no longer at %s, where it was written; it was moved or deleted outside Dockmesh",
			run.TargetPath, run.ID, where)
	}
	notFound := missing
	if certain {
		notFound = gone
	}
	msg := fmt.Sprintf("archive %s of run %d was not found at %s", run.TargetPath, run.ID, where)
	if job.TargetID > 0 {
		if t, err := st.targets().Get(ctx, job.TargetID); err == nil && t.UpdatedAt.After(run.StartedAt) {
			msg += fmt.Sprintf(": the run was taken on %s, before Dockmesh recorded where each run writes, and backup target %q was changed on %s, so the archive is most likely still where that target pointed before the change",
				run.StartedAt.Format(stampTime), t.Name, t.UpdatedAt.Format(stampTime))
			// A target saved from a job's inline settings on upgrade
			// was created after the run but points where the run wrote.
			if t.UpdatedAt.After(t.CreatedAt) {
				notFound = missing
			}
			return notFound("%s", msg)
		}
	}
	if job.UpdatedAt.After(run.StartedAt) {
		msg += fmt.Sprintf(": the run was taken on %s, before Dockmesh recorded where each run writes, and the job was changed on %s, so the archive is most likely on the target the job used before",
			run.StartedAt.Format(stampTime), job.UpdatedAt.Format(stampTime))
	}
	return notFound("%s", msg)
}

// guessedArchiveGone decides for a run without a certain location whose
// archive is not on the job's target: gone (with the reason) only when
// the run has a hash and no reachable saved target holds the file.
func guessedArchiveGone(ctx context.Context, st *store, n *notices.Service, run *Run, job *Job, where string) (string, bool, error) {
	if run.SHA256 == "" {
		return "", false, nil
	}
	rc, _, unreachable, err := searchSavedTargets(ctx, st, n, run)
	if err != nil {
		return "", false, err
	}
	if rc != nil {
		_ = rc.Close()
		return "", false, nil
	}
	nf := archiveNotFound(ctx, st, run, job, []string{where}, !unreachable)
	return nf.Error(), errors.Is(nf, ErrArchiveGone), nil
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
// which is the last moment it is known. Runs that started before their
// job last switched targets were written elsewhere and stay unstamped;
// switches before this was recorded are unknown, so the stamp stays a
// guess that reads and retention check.
func (s *store) stampLegacyRuns(ctx context.Context, loc *RunLocation, targetID, jobID int64) error {
	now := time.Now().UTC()
	stamp := *loc
	stamp.Inferred, stamp.ChangedAt = true, &now
	tid, raw := marshalLocation(&stamp)
	_, err := s.db.ExecContext(ctx, `
		UPDATE backup_runs SET target_id = ?, target_location = ?
		WHERE target_location IS NULL AND pruned_at IS NULL AND COALESCE(target_path, '') != ''
		  AND status != 'running'
		  AND (? = 0 OR job_id IN (SELECT id FROM backup_jobs WHERE target_id = ?))
		  AND (? = 0 OR job_id = ?)
		  AND started_at >= COALESCE(
		      (SELECT target_changed_at FROM backup_jobs WHERE id = backup_runs.job_id), '')`,
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
