package host

import (
	"slices"
	"testing"
)

func TestMountinfoContainerIDs(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	other := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	tests := []struct {
		name, content string
		want          []string
	}{
		{"inside a container", "612 590 8:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw - ext4 /dev/sda1 rw", []string{id}},
		{"host sees other containers' shm", "880 30 0:61 / /var/lib/docker/containers/" + other + "/mounts/shm rw - tmpfs shm rw", nil},
		{"image layer", "590 1 0:52 / / rw - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/" + other + "/diff", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mountinfoContainerIDs(tt.content); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
