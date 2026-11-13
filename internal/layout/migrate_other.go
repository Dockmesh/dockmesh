//go:build !unix

package layout

import "errors"

// Migrate fails off Unix; the config stays where it is.
func Migrate(MigrateOptions) (Result, error) {
	return Result{}, errors.New("moving the config is only supported on Linux")
}
