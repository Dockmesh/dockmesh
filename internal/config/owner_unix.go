//go:build unix

package config

import (
	"errors"
	"os"
	"syscall"
)

// keepOwner gives f the owner, group and permission bits of old.
func keepOwner(f *os.File, old os.FileInfo) error {
	st, ok := old.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine owner")
	}
	if err := f.Chown(int(st.Uid), int(st.Gid)); err != nil {
		return err
	}
	return f.Chmod(old.Mode().Perm())
}
