//go:build !unix

package main

import (
	"context"
	"errors"
	"os"
)

func applySetupAsRoot(context.Context, *setupFileRun, bool) (appliedSetup, error) {
	return appliedSetup{}, errors.New("a setup file is applied as root only on Linux")
}

func openOperatorFile(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- named by the operator's setup file
}
