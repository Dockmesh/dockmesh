package setup

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Disk is one mounted filesystem the data dir step offers. It names
// nothing below the mountpoint but the suggested target.
type Disk struct {
	Mountpoint string `json:"mountpoint"`
	FSType     string `json:"fs_type"`
	TotalBytes int64  `json:"total_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
	Suggested  string `json:"suggested"`
	Usable     bool   `json:"usable"`
	// Reason says why a disk is not usable.
	Reason string `json:"reason,omitempty"`
}

var (
	pseudoFS = map[string]bool{
		"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true, "ramfs": true,
		"cgroup": true, "cgroup2": true, "overlay": true, "squashfs": true, "nsfs": true, "debugfs": true,
		"tracefs": true, "securityfs": true, "pstore": true, "bpf": true, "autofs": true, "mqueue": true,
		"hugetlbfs": true, "fusectl": true, "configfs": true, "efivarfs": true, "binfmt_misc": true,
		"rpc_pipefs": true, "nfsd": true, "selinuxfs": true, "fuse.lxcfs": true, "fuse.portal": true,
	}
	networkFS = map[string]bool{
		"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "fuse.sshfs": true,
		"glusterfs": true, "ceph": true, "fuse.rclone": true, "9p": true, "davfs": true, "fuse.davfs": true,
	}
	noPermFS = map[string]bool{
		"vfat": true, "msdos": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true, "iso9660": true,
	}
	systemMounts = []string{"/proc", "/sys", "/dev", "/run", "/var/lib/docker", "/snap"}
)

// ListDisks returns the mounted filesystems from /proc/self/mountinfo,
// without pseudo filesystems and Docker's own directories.
func ListDisks() ([]Disk, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Disk
	// Skip bind-mounted files such as a container's /etc/hosts.
	for _, d := range parseDisks(f, diskUsage) {
		if fi, err := os.Stat(d.Mountpoint); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	return out, nil
}

func parseDisks(r io.Reader, usage func(string) (int64, int64, error)) []Disk {
	seen := map[string]bool{}
	var out []Disk
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 5 || sep+1 >= len(fields) {
			continue
		}
		mp := unescapeMount(fields[4])
		fsType := fields[sep+1]
		if pseudoFS[fsType] || seen[mp] || underAny(mp, systemMounts) {
			continue
		}
		seen[mp] = true
		d := Disk{Mountpoint: mp, FSType: fsType, Usable: true}
		d.TotalBytes, d.FreeBytes, _ = usage(mp)
		d.Suggested = filepath.Join(mp, "dockmesh")
		if mp == "/" {
			d.Suggested = "/var/lib/dockmesh"
		}
		switch {
		case networkFS[fsType] || strings.HasPrefix(fsType, "nfs"):
			d.Usable, d.Reason = false, "network share"
		case noPermFS[fsType]:
			d.Usable, d.Reason = false, "no Unix permissions"
		case CheckDataDirPath(d.Suggested) != nil:
			d.Usable, d.Reason = false, "not allowed as a data dir"
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	return out
}

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// unescapeMount undoes the octal escapes mountinfo uses for space, tab,
// newline and backslash.
func unescapeMount(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}
