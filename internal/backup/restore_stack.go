package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// stackRestoreFileSizeCap protects against a malicious or corrupted
// archive trying to write huge files into the stacks directory. 32 MiB
// per file is generous for compose / .env / supplemental config files
// (the original tar wrote them straight from disk, so anything larger
// than this should never have been there in the first place).
const stackRestoreFileSizeCap = 32 * 1024 * 1024

// stackRestoreMaxEntries caps the number of tar entries we'll process,
// guarding against zip-bomb-style inflation.
const stackRestoreMaxEntries = 50_000

// Steps of a restore, in order, as reported to its operation.
const (
	StepRestoreDownload = "download"
	StepRestoreVerify   = "verify"
	StepRestoreCheck    = "check_free"
	StepRestoreWrite    = "restore"
)

// RestoreSteps lists the steps Restore reports.
var RestoreSteps = []operations.StepSpec{
	{Key: StepRestoreDownload, Label: "Download archive"},
	{Key: StepRestoreVerify, Label: "Verify archive"},
	{Key: StepRestoreCheck, Label: "Check the volumes are not in use"},
	{Key: StepRestoreWrite, Label: "Restore files and volumes"},
}

// RestoreOptions picks where and how a run is restored.
type RestoreOptions struct {
	// Target is the volume (volume runs) or stack (stack runs) to restore
	// into; empty means the one the backup was taken from.
	Target string
	// HostID is the Docker host the volumes go to; empty means the host
	// the backup was taken on.
	HostID string
	// Replace empties each volume before extracting. Otherwise the
	// archive is merged: its files overwrite, other files stay.
	Replace bool
}

// RestoreReport says what a restore did.
type RestoreReport struct {
	Kind            string   `json:"kind"` // "volume" | "stack"
	StackName       string   `json:"stack_name,omitempty"`
	HostID          string   `json:"host_id"`
	Mode            string   `json:"mode"` // "replace" | "merge"
	FilesRestored   []string `json:"files_restored"`
	VolumesRestored []string `json:"volumes_restored"`
	Warnings        []string `json:"warnings,omitempty"`
}

// ErrArchiveInvalid is returned when the pre-restore check of the archive
// fails; nothing was changed.
var ErrArchiveInvalid = errors.New("backup archive failed verification, nothing was restored")

var (
	// ErrRestoreBusy refuses a restore while another one writes the same
	// volume or stack, or too many restores run.
	ErrRestoreBusy = errors.New("another restore is in progress")
	// ErrUnverifiedReplace refuses emptying volumes for a run with no
	// recorded checksum: nothing ties its archive to what was backed up.
	ErrUnverifiedReplace = errors.New("this backup has no recorded checksum, so it can only be merged, not replace the volume's contents")
)

const (
	maxConcurrentRestores = 2
	restoreTimeout        = 6 * time.Hour
)

// VolumeInUseError refuses replacing a volume that running containers use.
type VolumeInUseError struct {
	Volume     string
	Containers []string
}

func (e *VolumeInUseError) Error() string {
	return fmt.Sprintf("volume %s is used by running containers (%s); stop them before replacing its contents",
		e.Volume, strings.Join(e.Containers, ", "))
}

// Restore restores a volume or stack run. The archive is downloaded once,
// checked against the recorded size and hash, verified completely and
// then extracted from that same copy, so a damaged or swapped backup
// never overwrites anything.
func (s *Service) Restore(ctx context.Context, runID int64, opts RestoreOptions) (*RestoreReport, error) {
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if HasSystemSource(run.Sources) {
		return nil, ErrSystemRestore
	}
	if !run.Restorable() {
		return nil, ErrNotRestorable
	}
	if len(run.Sources) == 0 {
		return nil, errors.New("run has no sources")
	}
	kind := run.Sources[0].Type
	if kind != "volume" && kind != "stack" {
		return nil, fmt.Errorf("cannot restore a %q backup into a volume or stack", kind)
	}
	if opts.Replace && run.SHA256 == "" {
		return nil, ErrUnverifiedReplace
	}
	source := run.Sources[0].Name
	if opts.Target == "" {
		opts.Target = source
	}
	if kind == "volume" {
		if err := docker.CheckVolumeName(opts.Target); err != nil {
			return nil, err
		}
	} else if err := stacks.ValidateName(opts.Target); err != nil {
		return nil, fmt.Errorf("invalid stack name: %w", err)
	}
	job, err := s.store.getJobForRun(ctx, run.JobID)
	if err != nil {
		return nil, err
	}
	if opts.HostID == "" {
		opts.HostID = defaultStr(job.HostID, "local")
	}
	host, err := s.exec.resolveHost(opts.HostID)
	if err != nil {
		return nil, fmt.Errorf("restore host: %w", err)
	}
	op := operations.FromContext(ctx)
	// Download and verify only read; a user may stop the restore until
	// the first byte is written.
	readCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	op.AllowCancel(stop)
	stored, check, err := s.fetchVerified(readCtx, run, job, kind)
	op.DisallowCancel(readCtx)
	if cause := stopCause(readCtx); cause != nil {
		if stored != nil {
			_ = stored.Close()
		}
		return nil, fmt.Errorf("restore stopped before anything was written: %w", cause)
	}
	if err != nil {
		return nil, err
	}
	defer stored.Close()

	report := &RestoreReport{Kind: kind, HostID: opts.HostID, Mode: "merge", FilesRestored: []string{}, VolumesRestored: []string{}}
	if opts.Replace {
		report.Mode = "replace"
	}
	// Volumes the verified archive holds, mapped onto the target. A volume
	// with a fixed name belongs to whatever else uses that name, so a
	// restore under another stack name leaves it alone.
	plan := map[string]string{}
	if kind == "volume" {
		plan[source] = opts.Target
	} else {
		for _, v := range check.Counts.Volumes {
			dest := stackVolumeName(source, opts.Target, v)
			if dest == v && source != opts.Target {
				report.Warnings = append(report.Warnings, fmt.Sprintf("volume %s has a fixed name and was not restored into the copy %s", v, opts.Target))
				continue
			}
			plan[v] = dest
		}
	}
	keys := []string{"stack:" + opts.Target}
	for _, dest := range plan {
		keys = append(keys, "volume:"+opts.HostID+"/"+dest)
	}
	release, err := s.claimRestore(keys)
	if err != nil {
		return nil, err
	}
	defer release()
	if opts.Replace {
		op.StepStart(StepRestoreCheck, "")
		for _, dest := range plan {
			if err := checkVolumeFree(ctx, host, dest); err != nil {
				return nil, err
			}
		}
		op.StepDone(StepRestoreCheck, "")
	} else {
		op.StepSkip(StepRestoreCheck, "merge keeps existing data")
	}
	op.StepStart(StepRestoreWrite, "")

	plain, err := s.plaintext(stored, run)
	if err != nil {
		return nil, err
	}
	if kind == "volume" {
		if err := host.VolumeImport(ctx, opts.Target, plain, opts.Replace, check.diskNeed[""]); err != nil {
			return report, fmt.Errorf("restore volume %s: %w", opts.Target, err)
		}
		report.VolumesRestored = append(report.VolumesRestored, opts.Target)
		return report, nil
	}
	report.StackName = opts.Target
	return report, s.restoreStackInto(ctx, opts.Target, plain, host, plan, check.diskNeed, opts.Replace, report)
}

// fetchVerified downloads a run's archive into a temp copy and verifies
// it completely. The copy comes back, seeked to its start, whenever it
// was downloaded, so the caller always closes it.
func (s *Service) fetchVerified(ctx context.Context, run *Run, job *Job, kind string) (io.ReadSeekCloser, *VerifyResult, error) {
	op := operations.FromContext(ctx)
	op.StepStart(StepRestoreDownload, "")
	target, err := jobTarget(ctx, s.store, s.notices, job)
	if err != nil {
		return nil, nil, err
	}
	stored, err := fetchArchive(ctx, target, run)
	if err != nil {
		return nil, nil, err
	}
	op.StepDone(StepRestoreDownload, "")

	op.StepStart(StepRestoreVerify, "")
	check := &VerifyResult{RunID: run.ID, Type: kind, Passed: true}
	plain, err := s.plaintext(ctxReader{ctx: ctx, r: stored}, run)
	if err != nil {
		return stored, nil, err
	}
	if kind == "stack" {
		verifyStackArchive(check, plain)
	} else {
		verifyVolumeArchive(check, plain)
	}
	if err := ctx.Err(); err != nil {
		return stored, nil, err
	}
	if !check.Passed {
		return stored, nil, fmt.Errorf("%w: %s", ErrArchiveInvalid, failedChecks(check))
	}
	op.StepDone(StepRestoreVerify, "")
	if _, err := stored.Seek(0, io.SeekStart); err != nil {
		return stored, nil, err
	}
	return stored, check, nil
}

// ctxReader fails reads once ctx has ended.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// plaintext reads a fetched archive, decrypting encrypted runs. The
// returned reader doesn't close stored.
func (s *Service) plaintext(stored io.Reader, run *Run) (io.Reader, error) {
	if !run.Encrypted {
		return stored, nil
	}
	return wrapDecrypt(io.NopCloser(stored), s.secrets)
}

// checkVolumeFree refuses replacing a volume that containers use.
func checkVolumeFree(ctx context.Context, host hostBackupTarget, volume string) error {
	users, err := host.VolumeInUse(ctx, volume)
	if err != nil {
		return fmt.Errorf("check volume %s: %w", volume, err)
	}
	if len(users) > 0 {
		return &VolumeInUseError{Volume: volume, Containers: users}
	}
	return nil
}

// claimRestore takes the named volumes and stack for one restore, and a
// slot of the few restores that may run at once; each holds a temp copy
// of its archive.
func (s *Service) claimRestore(keys []string) (func(), error) {
	s.restoreMu.Lock()
	defer s.restoreMu.Unlock()
	if s.restoring == nil {
		s.restoring = map[string]bool{}
	}
	if s.restoreSlots >= maxConcurrentRestores {
		return nil, fmt.Errorf("%w: %d restores are already running", ErrRestoreBusy, s.restoreSlots)
	}
	for _, k := range keys {
		if s.restoring[k] {
			return nil, fmt.Errorf("%w: %s", ErrRestoreBusy, strings.TrimPrefix(strings.TrimPrefix(k, "stack:"), "volume:"))
		}
	}
	for _, k := range keys {
		s.restoring[k] = true
	}
	s.restoreSlots++
	return func() {
		s.restoreMu.Lock()
		defer s.restoreMu.Unlock()
		for _, k := range keys {
			delete(s.restoring, k)
		}
		s.restoreSlots--
	}, nil
}

// stackVolumeName maps a backed-up volume onto the target stack. Compose
// names a stack's volumes <stack>_<key>, so restoring as another stack
// must not write into the original's volumes. Explicitly named volumes
// keep their name.
func stackVolumeName(source, target, vol string) string {
	if source == target || !strings.HasPrefix(vol, source+"_") {
		return vol
	}
	return target + "_" + strings.TrimPrefix(vol, source+"_")
}

func failedChecks(r *VerifyResult) string {
	var out []string
	for _, c := range r.Checks {
		if c.Status == "fail" {
			out = append(out, c.Name+": "+c.Message)
		}
	}
	return strings.Join(out, "; ")
}

// restoreStackInto extracts a stack archive (layout in tarStackWithVolumes):
// stack/<rel> goes to the stack directory on the server, each
// volumes/<vol>.tar.gz named in plan is streamed into plan[vol] on host.
// Links in the archive are skipped; writes go through an os.Root.
func (s *Service) restoreStackInto(ctx context.Context, stackName string, src io.Reader, host hostBackupTarget, plan map[string]string, need map[string]int64, replace bool, report *RestoreReport) error {
	if s.stacks == nil {
		return errors.New("stacks manager not configured")
	}
	stackDir, err := s.stacks.Dir(stackName)
	if err != nil {
		return fmt.Errorf("resolve stack dir: %w", err)
	}
	if err := os.MkdirAll(stackDir, 0o755); err != nil {
		return fmt.Errorf("create stack dir: %w", err)
	}
	root, err := os.OpenRoot(stackDir)
	if err != nil {
		return fmt.Errorf("open stack dir: %w", err)
	}
	defer root.Close()

	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var composeContent, envContent string
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}
		entries++
		if entries > stackRestoreMaxEntries {
			return fmt.Errorf("archive has too many entries (>%d)", stackRestoreMaxEntries)
		}
		name := filepath.ToSlash(hdr.Name)
		switch {
		case strings.HasPrefix(name, "stack/"):
			rel := strings.TrimPrefix(name, "stack/")
			if rel == "" {
				continue
			}
			if !safeRelPath(rel) {
				report.Warnings = append(report.Warnings, fmt.Sprintf("skipped path traversal attempt: %s", name))
				continue
			}
			buf, err := restoreStackEntry(root, tr, hdr, rel, report)
			if err != nil {
				return err
			}
			switch rel {
			case "compose.yaml":
				composeContent = string(buf)
			case ".env":
				envContent = string(buf)
			}
		case strings.HasPrefix(name, "volumes/") && strings.HasSuffix(name, ".tar.gz"):
			volName := strings.TrimSuffix(strings.TrimPrefix(name, "volumes/"), ".tar.gz")
			if hdr.Typeflag != tar.TypeReg {
				return fmt.Errorf("archive entry %s is not a regular file", name)
			}
			dest, ok := plan[volName]
			if !ok {
				continue
			}
			if replace {
				if err := checkVolumeFree(ctx, host, dest); err != nil {
					return err
				}
			}
			operations.FromContext(ctx).Logf("info", "restoring volume %s", dest)
			if err := host.VolumeImport(ctx, dest, io.LimitReader(tr, hdr.Size), replace, need[volName]); err != nil {
				return fmt.Errorf("restore volume %s: %w", dest, err)
			}
			report.VolumesRestored = append(report.VolumesRestored, dest)
		}
	}
	if composeContent == "" {
		return errors.New("archive does not contain stack/compose.yaml")
	}
	// Update rewrites the same bytes but refreshes the manager's cache
	// and the recorded checksums.
	if _, err := s.stacks.Update(stackName, composeContent, envContent); err != nil {
		return fmt.Errorf("register restored stack with manager: %w", err)
	}
	slog.Info("stack restored from backup", "stack", stackName, "files", len(report.FilesRestored),
		"volumes", len(report.VolumesRestored), "warnings", len(report.Warnings))
	return nil
}

// restoreStackEntry writes one stack/<rel> entry and returns the content
// of regular files.
func restoreStackEntry(root *os.Root, tr *tar.Reader, hdr *tar.Header, rel string, report *RestoreReport) ([]byte, error) {
	dst := filepath.FromSlash(rel)
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := root.MkdirAll(dst, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", rel, err)
		}
	case tar.TypeReg:
		if hdr.Size > stackRestoreFileSizeCap {
			report.Warnings = append(report.Warnings, fmt.Sprintf("skipped oversize file (%d bytes): %s", hdr.Size, rel))
			return nil, nil
		}
		if err := root.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir parent for %s: %w", rel, err)
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		if err := root.WriteFile(dst, buf, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", rel, err)
		}
		report.FilesRestored = append(report.FilesRestored, rel)
		return buf, nil
	case tar.TypeSymlink, tar.TypeLink:
		report.Warnings = append(report.Warnings, fmt.Sprintf("skipped symlink: %s", rel))
	}
	return nil, nil
}

// safeRelPath rejects paths that try to escape the stack directory via
// .. or absolute prefixes. Only "stack/<simple-rel>" entries are
// accepted; the caller has already trimmed "stack/" before calling.
func safeRelPath(rel string) bool {
	if rel == "" {
		return false
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "\\") {
		return false
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." {
		return false
	}
	if strings.HasPrefix(cleaned, "..") {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// RunHost is the host a run's backup was taken on ("local" for the
// central server).
func (s *Service) RunHost(ctx context.Context, runID int64) (string, error) {
	run, err := s.store.getRun(ctx, runID)
	if err != nil {
		return "", err
	}
	job, err := s.store.getJobForRun(ctx, run.JobID)
	if err != nil {
		return "", err
	}
	return defaultStr(job.HostID, "local"), nil
}
