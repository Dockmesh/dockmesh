//go:build unix

package main

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// codeGroup is the group the server reads the code file with: dockmesh,
// else the group of the data dir (a container or a source build), else root.
func codeGroup(dataDir string) int {
	if g, err := user.LookupGroup("dockmesh"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			return gid
		}
	}
	if fi, err := os.Stat(dataDir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			return int(st.Gid)
		}
	}
	return 0
}
