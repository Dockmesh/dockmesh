//go:build unix

package restore

import (
	"os"
	"syscall"
)

// ownerOf returns the owner of the directory dir.
func ownerOf(dir string) (fileOwner, bool) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return fileOwner{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileOwner{}, false
	}
	return fileOwner{int(st.Uid), int(st.Gid)}, true
}
