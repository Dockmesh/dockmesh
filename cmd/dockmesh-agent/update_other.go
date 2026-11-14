//go:build !unix

package main

import (
	"errors"
	"fmt"
	"os"
)

const oNoFollow = 0

func rootOwnedBinary(string) bool { return false }

func runUpdateCmd([]string) {
	fmt.Fprintln(os.Stderr, "update apply: updates are applied by systemd on Linux only")
	os.Exit(1)
}

func legacyCopyStep(string) error { return errors.New("managed agents need Linux") }
