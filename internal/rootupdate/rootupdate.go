// Package rootupdate is the privileged half of a Dockmesh binary update:
// the root oneshot behind `dockmesh update apply`.
//
// The unprivileged service downloads a release into its request directory
// and writes RequestFile there. Everything in that directory is untrusted:
// Apply reads only the target version from the request, copies the release
// tarball into a root-only state directory, verifies the minisign signature
// of checksums.txt and the checksum of exactly that copy, and installs files
// extracted from that copy. It then stops the service, snapshots its data,
// swaps the files in (keeping <binary>.prev), rewrites the unit, starts the
// service and waits for it to report the new version. Any failure after the
// stop restores files, unit and data and starts the old version again.
//
// The role-specific parts (systemd calls, unit writer, data snapshot,
// health probe) come in through the Service interface.
package rootupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dockmesh/dockmesh/internal/releasesig"
)

// Files in the request directory. The service writes RequestFile and the
// release assets; Apply writes PendingFile while an update is in flight
// and ResultFile when it ends.
const (
	RequestFile = "request"
	PendingFile = "pending"
	ResultFile  = "result.json"
)

// Size caps for what Apply reads from the request directory.
const (
	MaxRequestSize   = 4 << 10
	MaxChecksumsSize = 64 << 10
	MaxSignatureSize = 4 << 10
	// DefaultMaxArtifactSize bounds the release tarball copy.
	DefaultMaxArtifactSize = 512 << 20
	// maxFileSize bounds one file extracted from the tarball.
	maxFileSize = 384 << 20
)

// DefaultHealthTimeout is how long the new version gets to report healthy.
const DefaultHealthTimeout = 180 * time.Second

// Request is what the service asks for. Only Version is read.
type Request struct {
	Version string `json:"version"`
}

// Pending is written to PendingFile from the stop of the old version until
// the update is confirmed or rolled back.
type Pending struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Started time.Time `json:"started"`
}

// Status is the outcome of an Apply run.
type Status string

const (
	// StatusUpdated: the new version runs and reported healthy.
	StatusUpdated Status = "updated"
	// StatusRefused: nothing was changed; the request or its files were
	// refused before the service was stopped.
	StatusRefused Status = "refused"
	// StatusRolledBack: the new version failed and the old one runs again
	// with its files, unit and data restored.
	StatusRolledBack Status = "rolled_back"
	// StatusFailed: the rollback itself failed; someone has to look.
	StatusFailed Status = "failed"
)

// Result is written to ResultFile when Apply ends.
type Result struct {
	Status   Status    `json:"status"`
	From     string    `json:"from"`
	To       string    `json:"to,omitempty"`
	Step     string    `json:"step,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Finished time.Time `json:"finished"`
}

// File is one file of the release tarball an update installs.
type File struct {
	// Name is the entry name in the tarball.
	Name string
	// Dest is the absolute install path. Its directory must belong to
	// root and be writable by nobody else.
	Dest string
	// Optional files missing from the tarball, or whose directory does not
	// exist, are left as they are.
	Optional bool
	// KeepPrev keeps the replaced file as <Dest>.prev; set for the binary
	// the service runs.
	KeepPrev bool
}

// Service is the role-specific side of an update. Every method acts on
// the one service the update is for.
type Service interface {
	Stop(ctx context.Context) error
	Start(ctx context.Context) error
	// ReadUnit returns the current unit files as an opaque blob.
	ReadUnit() ([]byte, error)
	// RestoreUnit puts back what ReadUnit returned.
	RestoreUnit(data []byte) error
	// WriteUnit writes the unit for the newly installed binary.
	WriteUnit(ctx context.Context, binary string) error
	DaemonReload(ctx context.Context) error
	// Snapshot takes a consistent copy of the service's data while it is
	// stopped; RestoreSnapshot puts it back. Both run the old binary.
	Snapshot(ctx context.Context) error
	RestoreSnapshot(ctx context.Context) error
	// Healthy reports whether the running service answers as version.
	Healthy(ctx context.Context, version string) bool
}

// Config describes one role's update.
type Config struct {
	// Installed is the version of the running (old) binary.
	Installed string
	// RequestDir is the service's update directory, owned by ServiceUID.
	RequestDir string
	ServiceUID int
	// StateDir is root-only; it holds the tarball copy, the journal and
	// the backups of replaced files.
	StateDir string
	// Asset is the release tarball name, e.g. dockmesh_linux_amd64.tar.gz.
	Asset string
	// Files to install; the first must be the service's binary.
	Files    []File
	Verifier *releasesig.Verifier
	Service  Service
	Log      *slog.Logger

	// Zero values pick the defaults.
	MaxArtifactSize int64
	HealthTimeout   time.Duration
	HealthInterval  time.Duration
	Now             func() time.Time
}

func (c *Config) defaults() error {
	if len(c.Files) == 0 || c.Files[0].Optional || !c.Files[0].KeepPrev {
		return errors.New("rootupdate: the first file must be the service binary with KeepPrev")
	}
	if c.Verifier == nil || c.Service == nil || c.RequestDir == "" || c.StateDir == "" || c.Asset == "" {
		return errors.New("rootupdate: incomplete config")
	}
	if c.MaxArtifactSize == 0 {
		c.MaxArtifactSize = DefaultMaxArtifactSize
	}
	if c.HealthTimeout == 0 {
		c.HealthTimeout = DefaultHealthTimeout
	}
	if c.HealthInterval == 0 {
		c.HealthInterval = 2 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return nil
}

// Steps named in a Result.
const (
	StepRequest  = "request"
	StepVerify   = "verify"
	StepStage    = "stage"
	StepStop     = "stop"
	StepSnapshot = "snapshot"
	StepInstall  = "install"
	StepUnit     = "unit"
	StepStart    = "start"
	StepHealth   = "health"
	StepRecover  = "recover"
)

// ErrRefused marks a request Apply turned down without changing anything.
var ErrRefused = errors.New("update refused")

func refused(step, format string, args ...any) *stepError {
	return &stepError{step: step, err: fmt.Errorf("%w: "+format, append([]any{ErrRefused}, args...)...)}
}

type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func failed(step string, err error) *stepError { return &stepError{step: step, err: err} }
