package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/releasesig"
	"github.com/dockmesh/dockmesh/internal/rootupdate"
	"github.com/dockmesh/dockmesh/internal/semver"
	"github.com/dockmesh/dockmesh/internal/stacklock"
)

// ResultNoticeKey is the notice raised from the root updater's result.
const ResultNoticeKey = "update.result"

// UnconfirmedNoticeKey is raised while an update stays unconfirmed for
// longer than the root updater ever needs, e.g. after it was killed.
const UnconfirmedNoticeKey = "update.unconfirmed"

// unconfirmedAfter is when an update in flight counts as stuck: the
// updater's own limit (TimeoutStartSec) plus some slack.
var unconfirmedAfter = 65 * time.Minute

// Default waits of an install.
const (
	// DefaultLockWait is how long an install waits for running stack
	// operations before it gives up.
	DefaultLockWait = 10 * time.Minute
	// DefaultApplyWait is how long the server waits for the root updater
	// to pick up the request before it releases the stacks again.
	DefaultApplyWait = 5 * time.Minute
)

// Install phases reported by InstallState.
const (
	PhaseIdle        = "idle"
	PhaseDownloading = "downloading"
	PhaseWaiting     = "waiting_for_stacks"
	PhaseApplying    = "applying"
	PhaseFailed      = "failed"
)

var (
	ErrInstallRunning   = errors.New("an update is already being installed")
	ErrUpdaterMissing   = errors.New("the root updater is not installed; upgrade with the installer once to set it up")
	ErrNotNewer         = errors.New("version is not newer than the running one")
	ErrInvalidVersion   = errors.New("not a release version")
	ErrInContainer      = errors.New("the server runs in a container; pull the new image and recreate the container instead")
	ErrChannel          = errors.New("pre-releases are only installed on the pre-release update channel")
	errStacksStillBusy  = errors.New("stack operations were still running after the wait; nothing was installed")
	errUpdaterNoAnswer  = errors.New("the root updater did not pick up the request; check dockmesh-update.path")
	errDownloadTooLarge = errors.New("download larger than allowed")
)

// Reasons an Installer can't install updates on this host.
const (
	UnavailableContainer   = "container"
	UnavailableNoUpdater   = "no_updater"
	UnavailableUnsupported = "unsupported"
)

// InstallState is what the server knows about the install it started.
type InstallState struct {
	Phase   string    `json:"phase"`
	Version string    `json:"version,omitempty"`
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started,omitzero"`
	// Pending is set while the root updater still checks a new version.
	Pending bool `json:"pending,omitempty"`
	// Unavailable names why Start would refuse every version.
	Unavailable string `json:"unavailable,omitempty"`
	// LastResult is the root updater's result this process read last.
	LastResult *rootupdate.Result `json:"last_result,omitempty"`
}

// InstallerConfig wires an Installer.
type InstallerConfig struct {
	// Dir is the update directory below the data dir (config.Paths.UpdateDir).
	Dir     string
	Current string
	Locks   *stacklock.Locks
	// Targets lists every host a stack may run on; an install takes the
	// maintenance lock on all of them, since the restart interrupts the
	// operations the server drives on agents too.
	Targets func(ctx context.Context) ([]stacklock.Target, error)
	Notices *notices.Service
	// Audit records the outcome of an update. It is called once per
	// result, again on the next read when it returned an error.
	Audit func(ctx context.Context, res rootupdate.Result) error
	// UpdaterInstalled reports whether the root updater's units exist.
	UpdaterInstalled func() bool
	// InContainer reports whether the server runs in a container, where
	// the image is updated instead.
	InContainer func() bool
	// Channel returns the update channel; pre-releases are refused on
	// any other than ChannelPrerelease.
	Channel func() string

	// Zero values pick the defaults.
	BaseURL   string
	Client    *http.Client
	Verifier  *releasesig.Verifier
	LockWait  time.Duration
	ApplyWait time.Duration
}

// Installer downloads a release into the update directory, checks it the
// same way the root updater will, holds every stack lock and writes the
// request. The root updater stops this process when it takes over.
type Installer struct {
	cfg InstallerConfig

	mu    sync.Mutex
	state InstallState
	last  *rootupdate.Result
}

// NewInstaller returns an Installer; the update directory is created if
// it is missing.
func NewInstaller(cfg InstallerConfig) (*Installer, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = releaseBaseURL()
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	if cfg.Verifier == nil {
		cfg.Verifier = releasesig.Default()
	}
	if cfg.LockWait == 0 && labLockWait != nil {
		cfg.LockWait = labLockWait()
	}
	if cfg.LockWait == 0 {
		cfg.LockWait = DefaultLockWait
	}
	if cfg.ApplyWait == 0 {
		cfg.ApplyWait = DefaultApplyWait
	}
	if cfg.UpdaterInstalled == nil {
		cfg.UpdaterInstalled = func() bool { return false }
	}
	if cfg.InContainer == nil {
		cfg.InContainer = func() bool { return false }
	}
	if cfg.Channel == nil {
		cfg.Channel = func() string { return ChannelStable }
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, err
	}
	// Temp files of a download a crash interrupted.
	if leftovers, err := filepath.Glob(filepath.Join(cfg.Dir, ".*")); err == nil {
		for _, f := range leftovers {
			_ = os.Remove(f)
		}
	}
	return &Installer{cfg: cfg, state: InstallState{Phase: PhaseIdle}}, nil
}

// Asset is the release archive for this server's platform.
func Asset() string { return fmt.Sprintf("dockmesh_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH) }

// State returns the state of the install started last.
func (i *Installer) State() InstallState {
	i.mu.Lock()
	st := i.state
	if i.last != nil {
		res := *i.last
		st.LastResult = &res
	}
	i.mu.Unlock()
	st.Pending = i.UpdatePending()
	st.Unavailable = i.unavailable()
	return st
}

func (i *Installer) unavailable() string {
	switch {
	case i.cfg.InContainer():
		return UnavailableContainer
	case !i.cfg.UpdaterInstalled():
		return UnavailableNoUpdater
	}
	return ""
}

func (i *Installer) set(phase string, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.state.Phase = phase
	i.state.Error = ""
	if err != nil {
		i.state.Error = err.Error()
	}
}

// Start begins installing version in the background. It refuses a version
// that is not newer than the running one, a second install, and a host
// without the root updater.
func (i *Installer) Start(ctx context.Context, version string) error {
	if !releasesig.ValidTag(version) {
		return ErrInvalidVersion
	}
	if !semver.Newer(version, i.cfg.Current) {
		return ErrNotNewer
	}
	if v, _ := semver.Parse(version); v.Prerelease() && i.cfg.Channel() != ChannelPrerelease {
		return ErrChannel
	}
	switch i.unavailable() {
	case UnavailableContainer:
		return ErrInContainer
	case UnavailableNoUpdater:
		return ErrUpdaterMissing
	}
	// Until the last update is confirmed or rolled back, the updater
	// would take a new request for the old one's result.
	if i.UpdatePending() {
		return ErrInstallRunning
	}
	i.mu.Lock()
	switch i.state.Phase {
	case PhaseDownloading, PhaseWaiting, PhaseApplying:
		i.mu.Unlock()
		return ErrInstallRunning
	}
	i.state = InstallState{Phase: PhaseDownloading, Version: version, Started: time.Now().UTC()}
	i.mu.Unlock()
	// The install outlives the request that started it.
	go i.run(context.WithoutCancel(ctx), version)
	return nil
}

func (i *Installer) run(ctx context.Context, version string) {
	if err := i.install(ctx, version); err != nil {
		slog.Warn("update install failed", "version", version, "err", err)
		i.set(PhaseFailed, err)
	}
}

func (i *Installer) install(ctx context.Context, version string) error {
	if err := i.download(ctx, version); err != nil {
		i.removeRelease()
		return err
	}
	i.set(PhaseWaiting, nil)
	var targets []stacklock.Target
	if i.cfg.Targets != nil {
		var err error
		if targets, err = i.cfg.Targets(ctx); err != nil {
			i.removeRelease()
			return fmt.Errorf("list the hosts to hold: %w", err)
		}
	}
	release, err := i.cfg.Locks.Maintenance(ctx, targets, stacklock.OpServerUpdate, i.cfg.LockWait)
	if err != nil {
		// The error names every busy stack; it goes to the log only.
		slog.Warn("update waits for stacks", "err", err)
		i.removeRelease()
		return errStacksStillBusy
	}
	defer release()
	// A result left from an earlier run must not be taken for this one's.
	if err := removeFile(i.cfg.Dir, rootupdate.ResultFile); err != nil {
		return err
	}
	req, _ := json.Marshal(rootupdate.Request{Version: version})
	if err := writeFileAtomic(i.cfg.Dir, rootupdate.RequestFile, req); err != nil {
		return err
	}
	i.set(PhaseApplying, nil)
	slog.Info("update requested", "version", version)
	// On success the updater stops this process before the wait ends. A
	// refusal ends it with a result and the server keeps running. Once the
	// updater took the request, the stacks stay held until it is done.
	deadline := time.Now().Add(i.cfg.ApplyWait)
	hardDeadline := deadline.Add(updaterRunLimit)
	for {
		if res, ok := i.resultFor(version); ok {
			res = i.CheckResult(ctx)
			if res.Status == rootupdate.StatusUpdated {
				i.set(PhaseIdle, nil)
				return nil
			}
			return fmt.Errorf("the update was %s: %s", res.Status, res.Reason)
		}
		now := time.Now()
		if now.After(deadline) {
			if _, err := os.Lstat(filepath.Join(i.cfg.Dir, rootupdate.RequestFile)); err == nil {
				_ = removeFile(i.cfg.Dir, rootupdate.RequestFile)
				i.removeRelease()
				return errUpdaterNoAnswer
			}
			if now.After(hardDeadline) {
				return errUpdaterNoAnswer
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// updaterRunLimit is how long the stacks stay held after the updater took
// the request: its own TimeoutStartSec.
const updaterRunLimit = 60 * time.Minute

// resultFor reads the result file if it is the one for version; a result
// of another update is left for AwaitResult.
func (i *Installer) resultFor(version string) (rootupdate.Result, bool) {
	var res rootupdate.Result
	data, err := readSmallFile(i.cfg.Dir, rootupdate.ResultFile, 64<<10)
	if err != nil || json.Unmarshal(data, &res) != nil {
		return res, false
	}
	return res, res.To == version
}

// removeRelease drops the downloaded release files of an install that
// ended before it wrote a request.
func (i *Installer) removeRelease() {
	for _, name := range []string{Asset(), releasesig.ChecksumsFile, releasesig.SignatureFile} {
		_ = removeFile(i.cfg.Dir, name)
	}
}

// download fetches the signature, checksums and archive of version into
// the update directory and checks them with the keys the root updater
// will use, so a bad download is refused before any stack waits.
func (i *Installer) download(ctx context.Context, version string) error {
	base := i.cfg.BaseURL + version + "/"
	checksums, err := i.fetch(ctx, base+releasesig.ChecksumsFile, rootupdate.MaxChecksumsSize)
	if err != nil {
		return err
	}
	sig, err := i.fetch(ctx, base+releasesig.SignatureFile, rootupdate.MaxSignatureSize)
	if err != nil {
		return err
	}
	if _, err := i.cfg.Verifier.VerifyChecksums(checksums, sig, version); err != nil {
		return err
	}
	asset := Asset()
	want, err := releasesig.Checksum(checksums, asset)
	if err != nil {
		return err
	}
	if err := i.fetchFile(ctx, base+asset, asset, want); err != nil {
		return err
	}
	if err := writeFileAtomic(i.cfg.Dir, releasesig.ChecksumsFile, checksums); err != nil {
		return err
	}
	return writeFileAtomic(i.cfg.Dir, releasesig.SignatureFile, sig)
}

func (i *Installer) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Dockmesh/"+i.cfg.Current)
	resp, err := i.cfg.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", filepath.Base(url), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: HTTP %d", filepath.Base(url), resp.StatusCode)
	}
	return resp, nil
}

func (i *Installer) fetch(ctx context.Context, url string, max int64) ([]byte, error) {
	resp, err := i.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w: %s", errDownloadTooLarge, filepath.Base(url))
	}
	return data, nil
}

// fetchFile streams url into name in the update directory, keeping it
// only when its SHA-256 is want.
func (i *Installer) fetchFile(ctx context.Context, url, name string, want []byte) error {
	resp, err := i.get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp, err := os.CreateTemp(i.cfg.Dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, rootupdate.DefaultMaxArtifactSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	if n > rootupdate.DefaultMaxArtifactSize {
		return fmt.Errorf("%w: %s", errDownloadTooLarge, name)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("%w: %s", releasesig.ErrChecksum, name)
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(i.cfg.Dir, name))
}

// UpdatePending reports whether the root updater has an update in flight:
// it wrote PendingFile and has not removed it yet.
func (i *Installer) UpdatePending() bool {
	_, err := os.Lstat(filepath.Join(i.cfg.Dir, rootupdate.PendingFile))
	return err == nil
}

// AwaitResult waits while an update is in flight, then reports its result
// through CheckResult. The server calls it once on start: right after an
// update it runs while the root updater still checks the new version.
func (i *Installer) AwaitResult(ctx context.Context) {
	// Counted from the marker, so a restart does not push the notice out.
	stuck := time.Now().Add(unconfirmedAfter)
	if fi, err := os.Lstat(filepath.Join(i.cfg.Dir, rootupdate.PendingFile)); err == nil {
		stuck = fi.ModTime().Add(unconfirmedAfter)
	}
	raised := false
	for i.UpdatePending() {
		if !raised && !time.Now().Before(stuck) && i.cfg.Notices != nil {
			raised = true
			if err := i.cfg.Notices.Raise(ctx, notices.RaiseInput{
				Key:      UnconfirmedNoticeKey,
				Audience: notices.Admins(),
				Severity: notices.SevCritical,
				Blocking: true,
				Title:    i18n.Msg("notice.update_unconfirmed.title"),
				Body:     i18n.Msg("notice.update_unconfirmed.body", "sudo systemctl start dockmesh-update.service"),
			}); err != nil {
				slog.Warn("raise update notice", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pendingPoll):
		}
	}
	// Also when an earlier process raised it.
	if i.cfg.Notices != nil {
		if err := i.cfg.Notices.Resolve(ctx, UnconfirmedNoticeKey); err != nil {
			slog.Warn("resolve update notice", "err", err)
		}
	}
	i.CheckResult(ctx)
}

// pendingPoll is how often AwaitResult looks for the end of an update.
var pendingPoll = 2 * time.Second

// CheckResult turns the root updater's result file into a notice for the
// admins, removes the file and the downloaded release, and returns the
// result (zero without a file).
func (i *Installer) CheckResult(ctx context.Context) rootupdate.Result {
	var res rootupdate.Result
	data, err := readSmallFile(i.cfg.Dir, rootupdate.ResultFile, 64<<10)
	if errors.Is(err, fs.ErrNotExist) {
		return res
	}
	if err == nil {
		err = json.Unmarshal(data, &res)
	}
	if err != nil {
		slog.Warn("read update result", "err", err)
		_ = removeFile(i.cfg.Dir, rootupdate.ResultFile)
		return res
	}
	slog.Info("update result", "status", res.Status, "from", res.From, "to", res.To, "step", res.Step, "reason", res.Reason)
	i.mu.Lock()
	last := res
	i.last = &last
	i.mu.Unlock()
	i.auditOnce(ctx, data, res)
	if i.cfg.Notices != nil {
		if err := i.cfg.Notices.Resolve(ctx, ResultNoticeKey); err != nil {
			slog.Warn("resolve update notice", "err", err)
		}
		if in, ok := resultNotice(res); ok {
			if err := i.cfg.Notices.Raise(ctx, in); err != nil {
				slog.Warn("raise update notice", "err", err)
			}
		}
	}
	if res.Status != rootupdate.StatusFailed {
		for _, name := range []string{rootupdate.ResultFile, Asset(), releasesig.ChecksumsFile, releasesig.SignatureFile} {
			_ = removeFile(i.cfg.Dir, name)
		}
	}
	return res
}

// auditedFile holds the hash of the last result passed to Audit. A failed
// result stays in place for the next root run, so every start reads it
// again.
const auditedFile = ".result-audited"

func (i *Installer) auditOnce(ctx context.Context, data []byte, res rootupdate.Result) {
	if i.cfg.Audit == nil {
		return
	}
	sum := sha256.Sum256(data)
	mark := []byte(hex.EncodeToString(sum[:]))
	if prev, err := readSmallFile(i.cfg.Dir, auditedFile, 128); err == nil && bytes.Equal(prev, mark) {
		return
	}
	if err := i.cfg.Audit(ctx, res); err != nil {
		return
	}
	if err := writeFileAtomic(i.cfg.Dir, auditedFile, mark); err != nil {
		slog.Warn("remember audited update result", "err", err)
	}
}

// resultNotice is the admin notice for res; an update that went through
// raises none.
func resultNotice(res rootupdate.Result) (notices.RaiseInput, bool) {
	in := notices.RaiseInput{Key: ResultNoticeKey, Audience: notices.Admins()}
	switch res.Status {
	case rootupdate.StatusRefused:
		in.Severity = notices.SevWarning
		in.Title = i18n.Msg("notice.update_refused.title", res.To)
		in.Body = i18n.Msg("notice.update_refused.body", res.Reason)
	case rootupdate.StatusRolledBack:
		in.Severity = notices.SevWarning
		in.Title = i18n.Msg("notice.update_rolled_back.title", res.To)
		in.Body = i18n.Msg("notice.update_rolled_back.body", res.From, res.Reason)
	case rootupdate.StatusFailed:
		in.Severity = notices.SevCritical
		in.Blocking = true
		in.Title = i18n.Msg("notice.update_failed.title", res.To)
		in.Body = i18n.Msg("notice.update_failed.body", res.Reason, "sudo systemctl start dockmesh-update.service")
	default:
		return in, false
	}
	return in, true
}

// writeFileAtomic replaces name in dir through a temp file and a rename.
func writeFileAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o640)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

func removeFile(dir, name string) error {
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// readSmallFile reads name in dir, at most max bytes.
func readSmallFile(dir, name string, max int64) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return data, nil
}
