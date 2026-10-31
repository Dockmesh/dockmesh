//go:build !windows

package pki

import (
	"os"
	"syscall"
)

// chownLike gives f the owner of ref when running as root, so files written
// by `sudo dockmesh ca ...` stay readable by the non-root service user.
func chownLike(f *os.File, ref string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	fi, err := os.Stat(ref)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return f.Chown(int(st.Uid), int(st.Gid))
}
