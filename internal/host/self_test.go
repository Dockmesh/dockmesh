package host

import (
	"slices"
	"testing"
)

func TestContainerIDCandidates(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	layer := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	tests := []struct {
		name            string
		content         string
		underContainers bool
		want            []string
	}{
		{"mountinfo hostname", "612 590 8:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw - ext4 /dev/sda1 rw", true, []string{id}},
		{"mountinfo layer only", "590 1 0:52 / / rw - overlay overlay rw,lowerdir=/var/lib/docker/overlay2/" + layer + "/diff", true, nil},
		{"cgroup v1", "12:memory:/docker/" + id, false, []string{id}},
		{"cgroup v2 host", "0::/user.slice", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containerIDCandidates(tt.content, tt.underContainers); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
