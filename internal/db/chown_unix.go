//go:build !windows

package db

import (
	"os"
	"syscall"
)

// chownLike gives path the owner of ref when running as root, so a
// snapshot taken by a root-run binary stays readable by the service user.
func chownLike(path, ref string) error {
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
	return os.Chown(path, int(st.Uid), int(st.Gid))
}
