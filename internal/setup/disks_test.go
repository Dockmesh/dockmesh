package setup

import (
	"strings"
	"testing"
)

const mountinfo = `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
23 22 0:5 / /proc rw - proc proc rw
24 22 0:22 / /run rw - tmpfs tmpfs rw
25 22 8:17 / /srv/backups rw - ext4 /dev/sdb1 rw
26 22 0:50 / /mnt/nas rw - nfs4 nas:/export rw
27 22 8:2 / /boot/efi rw - vfat /dev/sda2 rw
28 22 8:3 / /var/lib/docker rw - ext4 /dev/sda3 rw
29 28 0:60 / /var/lib/docker/overlay2/x/merged rw - overlay overlay rw
30 22 8:4 / /data\040disk rw - xfs /dev/sdc1 rw
31 22 8:5 / /srv/backups rw - ext4 /dev/sdb1 rw
`

func TestParseDisks(t *testing.T) {
	disks := parseDisks(strings.NewReader(mountinfo), func(string) (int64, int64, error) { return 100, 40, nil })
	got := map[string]Disk{}
	for _, d := range disks {
		got[d.Mountpoint] = d
	}
	for _, gone := range []string{"/proc", "/run", "/var/lib/docker", "/var/lib/docker/overlay2/x/merged"} {
		if _, ok := got[gone]; ok {
			t.Errorf("%s listed", gone)
		}
	}
	tests := []struct {
		mp, suggested string
		usable        bool
	}{
		{"/", "/var/lib/dockmesh", true},
		{"/srv/backups", "/srv/backups/dockmesh", true},
		{"/mnt/nas", "/mnt/nas/dockmesh", false},
		{"/boot/efi", "/boot/efi/dockmesh", false},
		{"/data disk", "/data disk/dockmesh", false},
	}
	for _, tt := range tests {
		d, ok := got[tt.mp]
		if !ok {
			t.Errorf("%s missing", tt.mp)
			continue
		}
		if d.Suggested != tt.suggested || d.Usable != tt.usable || d.FreeBytes != 40 {
			t.Errorf("%s: %+v", tt.mp, d)
		}
	}
	if len(disks) != len(tests) {
		t.Errorf("%d disks, want %d (duplicates?)", len(disks), len(tests))
	}
}
