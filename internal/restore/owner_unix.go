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

// chownDirNoFollow gives the directory dir to o; a symlink in its place is
// refused, not followed.
func chownDirNoFollow(dir string, o fileOwner) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chown(o.uid, o.gid)
}
