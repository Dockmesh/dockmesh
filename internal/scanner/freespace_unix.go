//go:build linux || darwin

package scanner

import "syscall"

// freeBytes is the space available in dir, ok=false when unknown.
func freeBytes(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true //nolint:unconvert // field types differ per OS
}
