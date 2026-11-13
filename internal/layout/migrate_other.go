//go:build !unix

package layout

import "errors"

// MigrateOptions configure Migrate.
type MigrateOptions struct {
	ConfigDir  string
	Candidates []string
	UID, GID   int
}

// Migrate is only supported on Unix hosts.
func Migrate(MigrateOptions) (Result, error) {
	return Result{}, errors.New("moving the config is only supported on Linux")
}
