//go:build unix

package rootupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/semver"
)

var tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

const journalName = "journal.json"

// journal records an update from the stop of the old version on, so a
// run that dies half-way can be rolled back by the next one (the update
// service also runs once at boot).
type journal struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Started   time.Time `json:"started"`
	Snapshot  bool      `json:"snapshot"`
	UnitSaved bool      `json:"unit_saved"`
	// Installed is set once every file and the unit are in place; only
	// then may a recovery keep the new version.
	Installed bool          `json:"installed"`
	Unit      []byte        `json:"unit,omitempty"`
	Files     []journalFile `json:"files,omitempty"`
}

// journalFile is one replaced file: Backup holds the old content, or
// Created says there was none and rollback removes Dest.
type journalFile struct {
	Dest    string `json:"dest"`
	Backup  string `json:"backup,omitempty"`
	Created bool   `json:"created,omitempty"`
}

// Apply handles one run of the update service: it finishes an update an
// earlier run left half-done, or else carries out the request in
// cfg.RequestDir. A run without either does nothing and returns a zero
// Result. The Result is also written to ResultFile.
func Apply(ctx context.Context, cfg Config) (Result, error) {
	if err := cfg.defaults(); err != nil {
		return Result{}, err
	}
	if err := ensureStateDir(cfg.StateDir); err != nil {
		return Result{}, fmt.Errorf("state dir: %w", err)
	}
	u := &updater{cfg: cfg}
	removeStale(cfg.Files)
	j, haveJournal, err := u.loadJournal()
	if err != nil {
		return Result{}, err
	}
	// The boot run finds no update directory on a server that never
	// staged an update; only a recovery goes on without one.
	req, err := openRequestDir(cfg.RequestDir, cfg.ServiceUID)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !haveJournal:
		return Result{}, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return Result{}, fmt.Errorf("request dir: %w", err)
	case err == nil:
		defer req.Close()
		u.req = req
	}

	var res Result
	if haveJournal {
		res = u.recover(ctx, j)
	} else {
		var ok bool
		res, ok = u.run(ctx)
		if !ok {
			return Result{}, nil
		}
	}
	res.Finished = cfg.Now().UTC()
	if err := u.writeJSON(ResultFile, res); err != nil {
		cfg.Log.Error("write update result", "err", err)
	}
	// The service reads the result once PendingFile is gone, so it goes
	// last. After a failed rollback it stays: the update is not over.
	if res.Status != StatusFailed && u.req != nil {
		if err := removeEntry(u.req, PendingFile); err != nil {
			cfg.Log.Warn("remove pending marker", "err", err)
		}
	}
	return res, nil
}

type updater struct {
	cfg Config
	req *os.Root
}

func (u *updater) result(status Status, to string, err error) Result {
	r := Result{Status: status, From: u.cfg.Installed, To: to}
	if err != nil {
		r.Reason = err.Error()
		var se *stepError
		if errors.As(err, &se) {
			r.Step, r.Reason = se.step, se.err.Error()
		}
	}
	return r
}

// run carries out a request. ok is false when there is none.
func (u *updater) run(ctx context.Context) (Result, bool) {
	to, err := u.readRequest()
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, false
	}
	if err != nil {
		u.cfg.Log.Warn("update request refused", "err", err)
		return u.result(StatusRefused, to, err), true
	}
	files, err := u.stage(to)
	if err != nil {
		u.cfg.Log.Warn("update refused", "to", to, "err", err)
		return u.result(StatusRefused, to, err), true
	}
	j := &journal{From: u.cfg.Installed, To: to, Started: u.cfg.Now().UTC()}
	if err := u.saveJournal(j); err != nil {
		discard(files)
		return u.result(StatusRefused, to, failed(StepStage, err)), true
	}
	if err := u.swap(ctx, j, files); err != nil {
		u.cfg.Log.Error("update failed, rolling back", "to", to, "err", err)
		return u.rollback(ctx, j, err), true
	}
	u.cfg.Log.Info("update applied", "from", j.From, "to", to)
	u.finish()
	return u.result(StatusUpdated, to, nil), true
}

// readRequest reads the target version and removes the request, whatever
// it is, so the path unit does not trigger again on the same one.
func (u *updater) readRequest() (string, error) {
	data, err := readUntrusted(u.req, RequestFile, MaxRequestSize)
	if errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if rerr := removeEntry(u.req, RequestFile); rerr != nil {
		return "", failed(StepRequest, fmt.Errorf("remove the request: %w", rerr))
	}
	if err != nil {
		return "", refused(StepRequest, "%v", err)
	}
	var r Request
	if err := json.Unmarshal(data, &r); err != nil {
		return "", refused(StepRequest, "request is not valid JSON")
	}
	to := r.Version
	if !tagPattern.MatchString(to) {
		return "", refused(StepRequest, "%q is not a release tag", to)
	}
	if _, ok := semver.Parse(u.cfg.Installed); !ok {
		return to, refused(StepRequest, "the installed version %q is not a release", u.cfg.Installed)
	}
	if !semver.Newer(to, u.cfg.Installed) {
		return to, refused(StepRequest, "%s is not newer than the installed %s", to, u.cfg.Installed)
	}
	return to, nil
}

// stage verifies the release for to and extracts its files next to their
// destinations. Nothing installed changes here.
func (u *updater) stage(to string) ([]staged, error) {
	checksums, err := readUntrusted(u.req, releasesig.ChecksumsFile, MaxChecksumsSize)
	if err != nil {
		return nil, refused(StepVerify, "%v", err)
	}
	sig, err := readUntrusted(u.req, releasesig.SignatureFile, MaxSignatureSize)
	if err != nil {
		return nil, refused(StepVerify, "%v", err)
	}
	if _, err := u.cfg.Verifier.VerifyChecksums(checksums, sig, to); err != nil {
		return nil, refused(StepVerify, "%v", err)
	}
	want, err := releasesig.Checksum(checksums, u.cfg.Asset)
	if err != nil {
		return nil, refused(StepVerify, "%v", err)
	}
	copyPath, err := copyArtifact(u.req, u.cfg.Asset, u.cfg.StateDir, u.cfg.MaxArtifactSize, want)
	if err != nil {
		return nil, refused(StepVerify, "%v", err)
	}
	defer os.Remove(copyPath)
	files, err := extract(copyPath, u.cfg.Files)
	if err != nil {
		return nil, refused(StepStage, "%v", err)
	}
	return files, nil
}

func discard(files []staged) {
	for _, f := range files {
		_ = os.Remove(f.tmp)
	}
}

// swap replaces the running version with the staged files and waits for
// it to report healthy. Every step is recorded in j before it changes
// anything, so rollback can undo exactly what was done.
func (u *updater) swap(ctx context.Context, j *journal, files []staged) error {
	defer discard(files)
	svc := u.cfg.Service
	pending := Pending{From: j.From, To: j.To, Started: j.Started}
	if err := u.writeJSON(PendingFile, pending); err != nil {
		return failed(StepStop, fmt.Errorf("write %s: %w", PendingFile, err))
	}
	if err := svc.Stop(ctx); err != nil {
		return failed(StepStop, err)
	}
	if err := svc.Snapshot(ctx); err != nil {
		return failed(StepSnapshot, err)
	}
	j.Snapshot = true
	if err := u.saveJournal(j); err != nil {
		return failed(StepSnapshot, err)
	}
	for i, f := range files {
		if err := u.install(j, i, f); err != nil {
			return failed(StepInstall, err)
		}
	}
	unit, err := svc.ReadUnit()
	if err != nil {
		return failed(StepUnit, err)
	}
	j.Unit, j.UnitSaved = unit, true
	if err := u.saveJournal(j); err != nil {
		return failed(StepUnit, err)
	}
	if err := svc.WriteUnit(ctx, u.cfg.Files[0].Dest); err != nil {
		return failed(StepUnit, err)
	}
	if err := svc.DaemonReload(ctx); err != nil {
		return failed(StepUnit, err)
	}
	j.Installed = true
	if err := u.saveJournal(j); err != nil {
		return failed(StepUnit, err)
	}
	if err := svc.Start(ctx); err != nil {
		return failed(StepStart, err)
	}
	if !u.waitHealthy(ctx, j.To) {
		return failed(StepHealth, fmt.Errorf("%s did not report healthy within %s", j.To, u.cfg.HealthTimeout))
	}
	return nil
}

// install backs up the file at f.Dest, records it, and renames the staged
// file into place.
func (u *updater) install(j *journal, i int, f staged) error {
	jf := journalFile{Dest: f.Dest}
	if _, err := os.Lstat(f.Dest); errors.Is(err, fs.ErrNotExist) {
		jf.Created = true
	} else if err != nil {
		return err
	} else {
		jf.Backup = filepath.Join(u.cfg.StateDir, "backup-"+strconv.Itoa(i))
		if f.KeepPrev {
			jf.Backup = f.Dest + ".prev"
		}
		if err := copyRootFile(f.Dest, jf.Backup, 0o755); err != nil {
			return fmt.Errorf("back up %s: %w", f.Dest, err)
		}
	}
	j.Files = append(j.Files, jf)
	if err := u.saveJournal(j); err != nil {
		return err
	}
	return os.Rename(f.tmp, f.Dest)
}

func (u *updater) waitHealthy(ctx context.Context, version string) bool {
	deadline := u.cfg.Now().Add(u.cfg.HealthTimeout)
	for {
		if u.cfg.Service.Healthy(ctx, version) {
			return true
		}
		if !u.cfg.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(u.cfg.HealthInterval):
		}
	}
}

// rollback undoes what j records and starts the old version. cause is
// what made the update fail.
func (u *updater) rollback(ctx context.Context, j *journal, cause error) Result {
	// The rollback must run to its end even when the run's context is gone.
	ctx = context.WithoutCancel(ctx)
	svc := u.cfg.Service
	res := u.result(StatusRolledBack, j.To, cause)
	res.From = j.From
	var errs []error
	// With nothing changed yet a failed stop is harmless: the old version
	// may still run, which its health check below shows. Otherwise files
	// and data are never restored under a running service; the journal
	// stays for the next run.
	if err := svc.Stop(ctx); err != nil {
		if len(j.Files) > 0 || j.Snapshot {
			res.Status = StatusFailed
			res.Reason += "; rollback: stop: " + err.Error()
			u.cfg.Log.Error("update rollback could not stop the service", "err", err)
			return res
		}
		u.cfg.Log.Warn("stop before rollback", "err", err)
	}
	for i := len(j.Files) - 1; i >= 0; i-- {
		f := j.Files[i]
		var err error
		if f.Created {
			err = os.Remove(f.Dest)
			if errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		} else {
			err = copyRootFile(f.Backup, f.Dest, 0o755)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", f.Dest, err))
		}
	}
	if j.UnitSaved {
		if err := svc.RestoreUnit(j.Unit); err != nil {
			errs = append(errs, fmt.Errorf("restore unit: %w", err))
		}
	}
	if err := svc.DaemonReload(ctx); err != nil {
		errs = append(errs, err)
	}
	if j.Snapshot {
		if err := svc.RestoreSnapshot(ctx); err != nil {
			errs = append(errs, fmt.Errorf("restore data: %w", err))
		}
	}
	if err := svc.Start(ctx); err != nil {
		errs = append(errs, fmt.Errorf("start: %w", err))
	} else if !u.waitHealthy(ctx, j.From) {
		errs = append(errs, fmt.Errorf("%s did not report healthy after the rollback", j.From))
	}
	if len(errs) > 0 {
		// The journal and pending marker stay, so the next run (at the
		// latest the one at boot) tries the rollback again.
		res.Status = StatusFailed
		res.Reason += "; rollback: " + errors.Join(errs...).Error()
		u.cfg.Log.Error("update rollback failed", "from", j.From, "to", j.To, "err", errors.Join(errs...))
		return res
	}
	u.cfg.Log.Warn("update rolled back", "from", j.From, "to", j.To)
	u.finish()
	return res
}

// recover finishes an update an earlier run did not: it is kept when the
// new version answers, else rolled back.
func (u *updater) recover(ctx context.Context, j *journal) Result {
	u.cfg.Log.Warn("finishing an interrupted update", "from", j.From, "to", j.To)
	// A request made meanwhile is moot; left in place it would start the
	// next run right away.
	if u.req != nil {
		if err := removeEntry(u.req, RequestFile); err != nil {
			u.cfg.Log.Warn("remove update request", "err", err)
		}
	}
	if j.Installed && u.waitHealthy(ctx, j.To) {
		u.finish()
		r := u.result(StatusUpdated, j.To, nil)
		r.From, r.Step = j.From, StepRecover
		return r
	}
	return u.rollback(ctx, j, failed(StepRecover, errors.New("an earlier update run was interrupted")))
}

// finish drops the journal and the backups. The <binary>.prev copy stays.
func (u *updater) finish() {
	matches, _ := filepath.Glob(filepath.Join(u.cfg.StateDir, "backup-*"))
	for _, m := range matches {
		_ = os.Remove(m)
	}
	if err := os.Remove(filepath.Join(u.cfg.StateDir, journalName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		u.cfg.Log.Warn("remove update journal", "err", err)
	}
}

func (u *updater) loadJournal() (*journal, bool, error) {
	data, err := os.ReadFile(filepath.Join(u.cfg.StateDir, journalName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, false, fmt.Errorf("read %s: %w", journalName, err)
	}
	return &j, true, nil
}

func (u *updater) saveJournal(j *journal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	path := filepath.Join(u.cfg.StateDir, journalName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (u *updater) writeJSON(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if u.req == nil {
		return errors.New("no update directory")
	}
	return writeServiceFile(u.req, name, append(data, '\n'))
}
