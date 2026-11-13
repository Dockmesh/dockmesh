//go:build unix

package main

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// codeGroup is the group the server reads the code file with: the group
// of its data dir (the installer's unit runs it with that group, as does a
// container or a source build), else dockmesh, else root.
func codeGroup(dataDir string) int {
	if fi, err := os.Stat(dataDir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Gid != 0 {
			return int(st.Gid)
		}
	}
	if g, err := user.LookupGroup("dockmesh"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			return gid
		}
	}
	return 0
}
