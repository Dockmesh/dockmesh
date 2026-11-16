//go:build !unix

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dockmesh/dockmesh/internal/agents"
)

const (
	oNoFollow  = 0
	oDirectory = 0
)

func rootOwnedBinary(string) bool { return false }

func runUpdateCmd([]string) {
	fmt.Fprintln(os.Stderr, "update apply: updates are applied by systemd on Linux only")
	os.Exit(1)
}

func runSystemCmd([]string) {
	fmt.Fprintln(os.Stderr, "system install: the agent is installed with systemd on Linux only")
	os.Exit(1)
}

func applyStacksDir(context.Context, agents.StacksDirApplyReq) (agents.StacksDirApplyRes, error) {
	return agents.StacksDirApplyRes{}, errors.New("the stacks directory is changed by the root updater on Linux only")
}

func legacyCopyStep(string) error { return errors.New("managed agents need Linux") }
