//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize installs a SIGWINCH handler that pushes onto ch whenever
// the controlling terminal resizes, so runExec can ship a new
// resize control frame to the container's TTY.
func watchResize(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}
