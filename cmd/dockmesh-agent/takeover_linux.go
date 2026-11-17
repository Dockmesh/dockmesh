package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// takeOverOpenFlags open an entry as a path only: a device node a
// container planted is never opened, and a symlink is the link itself.
const takeOverOpenFlags = unix.O_PATH | unix.O_NOFOLLOW

func chownOpened(f *os.File, uid, gid int) error {
	return unix.Fchownat(int(f.Fd()), "", uid, gid, unix.AT_EMPTY_PATH)
}
