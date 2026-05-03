//go:build !windows

package setup

import "syscall"

// diskUsage returns total + free bytes for the filesystem mounted at
// the given path. Linux/macOS/BSD path; Statfs has slightly different
// semantics across the family but Bavail*Bsize is the right "free for
// non-root processes" number on all of them.
func diskUsage(path string) (total, free int64, err error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, 0, err
	}
	total = int64(s.Blocks) * int64(s.Bsize)
	free = int64(s.Bavail) * int64(s.Bsize)
	return total, free, nil
}
