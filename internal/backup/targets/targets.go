// Package targets holds storage backend implementations for the backup
// system. The Target interface + Entry type live here (not in the parent
// backup package) so implementations don't pull in backup and create a
// cycle — backup imports targets, not the other way around.
package targets

import (
	"context"
	"io"
	"time"
)

// Target is the storage backend interface. Implementations: Local, S3.
type Target interface {
	Open(ctx context.Context, path string) (io.WriteCloser, error)
	Read(ctx context.Context, path string) (io.ReadCloser, error)
	List(ctx context.Context, prefix string) ([]Entry, error)
	Delete(ctx context.Context, path string) error
}

// Aborter is implemented by writers that upload on Close: Abort drops the
// upload so a failed run never publishes a truncated archive.
type Aborter interface {
	Abort() error
}

// Breaker is implemented by writers and readers on a network connection
// whose Close waits for the server: Break drops the connection at once,
// which ends a read or write blocked on a server that stopped answering.
// What was written so far stays on the target.
type Breaker interface {
	Break() error
}

// Break drops c's connection when it is a Breaker, else closes it.
func Break(c io.Closer) error {
	if b, ok := c.(Breaker); ok {
		return b.Break()
	}
	return c.Close()
}

// Entry is one backup file at the target, returned by List.
type Entry struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// Build constructs a live Target from a stored type + config. Exported
// so callers outside the backup package (e.g. audit retention) can
// reuse the same adapter code without importing backup and creating a
// cycle.
func Build(typ string, cfg any) (Target, error) {
	switch typ {
	case "local":
		return NewLocal(cfg)
	case "s3":
		return NewS3(cfg)
	case "sftp":
		return NewSFTP(cfg)
	case "smb":
		return NewSMB(cfg)
	case "webdav":
		return NewWebDAV(cfg)
	}
	return nil, errUnknownType
}

var errUnknownType = &targetError{"unknown target type"}

type targetError struct{ msg string }

func (e *targetError) Error() string { return e.msg }
