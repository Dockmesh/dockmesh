//go:build !unix

package stacksdir

import (
	"context"
	"errors"
	"io"

	"github.com/dockmesh/dockmesh/internal/agents"
)

var errUnsupported = errors.New("a stacks dir outside the data dir needs Linux")

// Check refuses every path where root cannot check one.
func Check(p string, _ Rules) Result {
	return Result{StacksDirCheck: agents.StacksDirCheck{Path: p, Status: StatusFail, Issue: Unreadable, Message: errUnsupported.Error()}}
}

// DockerRoots knows no Docker data roots here.
func DockerRoots(context.Context, string) []string { return nil }

// Create is not supported here.
func Create(string, int, int) error { return errUnsupported }

// ProcMountinfo is not supported here.
func ProcMountinfo() (io.ReadCloser, error) { return nil, errUnsupported }
