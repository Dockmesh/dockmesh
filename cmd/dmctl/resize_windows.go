//go:build windows

package main

import "os"

// watchResize is a no-op on Windows — the OS doesn't surface a resize
// signal. The initial window size still ships on session start, which
// is what 99% of interactive exec sessions actually need.
func watchResize(ch chan<- os.Signal) {}
