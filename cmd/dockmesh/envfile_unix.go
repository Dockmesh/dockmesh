//go:build unix

package main

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// readTrustedEnvFile reads an auto-detected env file only if root or the
// current user owns it and nobody else may write it, so a file planted
// or edited by another account cannot redirect a root CLI run.
func readTrustedEnvFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("%w: cannot determine owner", errUntrustedEnvFile)
	}
	if st.Uid != 0 && int(st.Uid) != os.Getuid() {
		return nil, fmt.Errorf("%w: owned by uid %d", errUntrustedEnvFile, st.Uid)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%w: writable by group or others", errUntrustedEnvFile)
	}
	return io.ReadAll(f)
}
