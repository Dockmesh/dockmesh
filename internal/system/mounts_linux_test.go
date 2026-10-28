//go:build linux

package system

import (
	"bufio"
	"strings"
	"testing"
)

func TestParseMountinfo(t *testing.T) {
	const info = `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
23 22 0:21 / /proc rw,nosuid shared:12 - proc proc rw
40 22 8:17 / /mnt/my\040disk rw,relatime shared:30 - xfs /dev/sdb1 rw
41 22 8:17 /sub /srv/bind rw,relatime shared:30 - xfs /dev/sdb1 rw
garbage line
`
	got := parseMountinfo(bufio.NewScanner(strings.NewReader(info)))
	want := []mountEntry{
		{key: "8:1", point: "/", fstype: "ext4", device: "/dev/sda1"},
		{key: "0:21", point: "/proc", fstype: "proc", device: "proc"},
		{key: "8:17", point: "/mnt/my disk", fstype: "xfs", device: "/dev/sdb1"},
		{key: "8:17", point: "/srv/bind", fstype: "xfs", device: "/dev/sdb1"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
