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

// Entry is one backup file at the target, returned by List.
type Entry struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}
