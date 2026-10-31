//go:build darwin

package system

import (
	"fmt"
	"syscall"
)

// mntDontBrowse marks the APFS system helper volumes (Preboot, VM,
// Update, …) that share the container of the data volume.
const mntDontBrowse = 0x00100000

func listMounts() []mountEntry {
	n, err := syscall.Getfsstat(nil, 2 /* MNT_NOWAIT */)
	if err != nil || n <= 0 {
		return nil
	}
	buf := make([]syscall.Statfs_t, n)
	n, err = syscall.Getfsstat(buf, 2)
	if err != nil {
		return nil
	}
	out := make([]mountEntry, 0, n)
	for _, st := range buf[:n] {
		if st.Flags&mntDontBrowse != 0 {
			continue
		}
		out = append(out, mountEntry{
			point:  cString(st.Mntonname[:]),
			device: cString(st.Mntfromname[:]),
			fstype: cString(st.Fstypename[:]),
			key:    fsidKey(st.Fsid),
		})
	}
	return out
}

func fsidKey(id syscall.Fsid) string { return fmt.Sprintf("%d:%d", id.Val[0], id.Val[1]) }

func cString(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}

func deviceKey(path string) (string, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", false
	}
	return fsidKey(st.Fsid), true
}
