package stackhelper

import (
	"strings"
	"testing"
)

func TestMountBelow(t *testing.T) {
	info := `22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw
30 22 0:40 / /srv/stacks rw - ext4 /dev/sdb1 rw
31 30 0:41 / /srv/stacks/plex/media\040share rw - nfs host:/media rw
32 22 0:42 / /srv/stacks/plexa rw - tmpfs tmpfs rw
`
	tests := []struct {
		dir  string
		want string
	}{
		{"/srv/stacks/plex", "media share"},
		{"/srv/stacks/web", ""},
		{"/srv/stacks/plexa", ""},
		{"/srv/stacks/plex/media share", ""},
	}
	for _, tt := range tests {
		got, err := mountBelowIn(strings.NewReader(info), tt.dir)
		if err != nil || got != tt.want {
			t.Errorf("mountBelowIn(%q) = %q, %v; want %q", tt.dir, got, err, tt.want)
		}
	}
}
