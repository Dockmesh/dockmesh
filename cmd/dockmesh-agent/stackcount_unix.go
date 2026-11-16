//go:build unix

package main

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// maxStackEntries bounds how many entries of a stacks directory are read
// to count its stacks.
const maxStackEntries = 10000

// countStacks returns the number of stacks in dir: directories, not
// symlinks, with a regular compose.yaml. A missing dir holds none. Root
// counts dirs the agent owns, so every step goes through a descriptor
// without following a symlink.
func countStacks(dir string) (int, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return 0, nil
	}
	if err != nil {
		return 0, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	f := os.NewFile(uintptr(fd), dir) // #nosec G115 -- a descriptor just opened
	defer f.Close()
	names, err := f.Readdirnames(maxStackEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	n := 0
	for _, name := range names {
		// O_DIRECTORY fails on a FIFO or device without blocking.
		sub, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		var st unix.Stat_t
		err = unix.Fstatat(sub, "compose.yaml", &st, unix.AT_SYMLINK_NOFOLLOW)
		unix.Close(sub)
		if err == nil && st.Mode&unix.S_IFMT == unix.S_IFREG {
			n++
		}
	}
	return n, nil
}
