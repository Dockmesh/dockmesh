package handlers

import (
	"testing"

	"github.com/dockmesh/dockmesh/internal/system"
)

func TestStacksDirDisks(t *testing.T) {
	const gib = 1 << 30
	mounts := []system.Mount{
		{MountPoint: "/", FSType: "ext4", Total: 30 * gib, Used: 9 * gib},
		{MountPoint: "/srv", FSType: "xfs", Total: 100 * gib, Used: 10 * gib},
		{MountPoint: "/mnt/disk1", FSType: "btrfs", Total: 50 * gib},
		{MountPoint: "/volume1", FSType: "ext4", Total: 10 * gib},
		{MountPoint: "/mnt/media", FSType: "nfs4", Total: 10 * gib},
		{MountPoint: "/mnt/usb", FSType: "exfat", Total: 10 * gib},
		{MountPoint: "/var/lib/docker", FSType: "ext4", Total: 10 * gib},
		{MountPoint: "/run", FSType: "tmpfs", Total: gib},
		{MountPoint: "/srv", FSType: "xfs"},
		{MountPoint: "/broken", FSType: "ext4", Error: "permission denied"},
	}
	type want struct {
		suggested string
		usable    bool
		reason    string
	}
	wants := map[string]want{
		"/":          {"/data/agent/stacks", true, ""},
		"/srv":       {"/srv/stacks", true, ""},
		"/mnt/disk1": {"/mnt/disk1/stacks", true, ""},
		"/volume1":   {"/volume1/stacks", false, "not allowed as a stacks dir"},
		"/mnt/media": {"/mnt/media/stacks", false, "network share"},
		"/mnt/usb":   {"/mnt/usb/stacks", false, "no Unix permissions"},
	}
	got := stacksDirDisks(mounts, "/data/agent")
	if len(got) != len(wants) {
		t.Fatalf("got %d disks %+v, want %d", len(got), got, len(wants))
	}
	for _, d := range got {
		w, ok := wants[d.Mountpoint]
		if !ok {
			t.Errorf("unexpected disk %s", d.Mountpoint)
			continue
		}
		if d.Suggested != w.suggested || d.Usable != w.usable || d.Reason != w.reason {
			t.Errorf("%s: %q usable=%v reason=%q, want %q %v %q", d.Mountpoint, d.Suggested, d.Usable, d.Reason, w.suggested, w.usable, w.reason)
		}
	}
	if got[0].FreeBytes != 21*gib {
		t.Errorf("free bytes of / = %d", got[0].FreeBytes)
	}
}
