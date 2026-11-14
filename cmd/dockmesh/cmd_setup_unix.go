//go:build unix

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/dockmesh/dockmesh/internal/layout"
)

// codeGroup is the group the server reads the code file with: the Group=
// of its unit (dockmesh once write-unit wrote it), else the dockmesh
// group, else the group of its data dir (a container or a source build),
// else root.
func codeGroup(dataDir string) int {
	if out, err := exec.Command("systemctl", "show", "-p", "Group", "--value", "dockmesh").Output(); err == nil {
		if gid, ok := lookupGID(strings.TrimSpace(string(out))); ok {
			return gid
		}
	}
	if gid, ok := lookupGID(layout.ServiceGroup); ok {
		return gid
	}
	if fi, err := os.Stat(dataDir); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			return int(st.Gid)
		}
	}
	return 0
}
