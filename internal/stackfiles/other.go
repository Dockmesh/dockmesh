//go:build !unix

package stackfiles

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("stack files are only copied on Linux and other Unix systems")

// Collect is not supported on this platform.
func Collect(string) (*Set, []Note, error) { return nil, nil, errUnsupported }

// Apply is not supported on this platform.
func Apply(*os.Root, *Set, *Manifest) (*Manifest, Report, error) {
	return nil, Report{}, errUnsupported
}
