//go:build linux || darwin || freebsd

package targets

import "syscall"

// diskUsage returns (total, used) bytes for the filesystem that holds
// path. Used by Local target's StorageInfo.
func diskUsage(path string) (int64, int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	total := int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bavail) * int64(st.Bsize)
	return total, total - free, nil
}
