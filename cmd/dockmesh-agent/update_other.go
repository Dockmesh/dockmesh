//go:build !unix

package main

import (
	"fmt"
	"os"
)

const oNoFollow = 0

func rootOwnedBinary(string) bool { return false }

func runUpdateCmd([]string) {
	fmt.Fprintln(os.Stderr, "update apply: updates are applied by systemd on Linux only")
	os.Exit(1)
}
