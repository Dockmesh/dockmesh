//go:build unix

package main

import (
	"io"
	"os"
	"syscall"
)

func readRootOwnedFile(path string) ([]byte, bool) {
	f, err := os.Open(path) // #nosec G304 -- fixed path of the installer's config
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o022 != 0 {
		return nil, false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	return data, err == nil
}
