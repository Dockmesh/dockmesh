//go:build unix

package config

import (
	"os"
	"syscall"
)

func sameOwner(a, b os.FileInfo) bool {
	sa, sb := a.Sys().(*syscall.Stat_t), b.Sys().(*syscall.Stat_t)
	return sa.Uid == sb.Uid && sa.Gid == sb.Gid
}
