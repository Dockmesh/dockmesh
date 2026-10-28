package system

import (
	"reflect"
	"testing"
	"time"
)

func TestBuildMounts(t *testing.T) {
	entries := []mountEntry{
		{point: "/", device: "/dev/sda1", fstype: "ext4", key: "8:1"},
		{point: "/proc", device: "proc", fstype: "proc", key: "0:20"},
		{point: "/run", device: "tmpfs", fstype: "tmpfs", key: "0:24"},
		{point: "/var/lib/docker/overlay2/x/merged", device: "overlay", fstype: "overlay", key: "0:50"},
		{point: "/var/lib/containerd", device: "/dev/sdb1", fstype: "xfs", key: "8:17"},
		{point: "/srv/bind-of-containerd", device: "/dev/sdb1", fstype: "xfs", key: "8:17"},
		{point: "/mnt/nfs", device: "nas:/backups", fstype: "nfs4", key: "0:60"},
	}
	keys := map[string]string{
		"/var/lib/docker":     "8:1",
		"/var/lib/containerd": "8:17",
		"/opt/stacks":         "8:1",
		"/mnt/nfs/dockmesh":   "0:60",
	}
	keyOf := func(p string) (string, bool) { k, ok := keys[p]; return k, ok }
	usage := map[string][3]float64{
		"/":                   {100, 5, 5},
		"/var/lib/containerd": {20, 20, 100},
	}
	use := func(p string) (uint64, uint64, float64, string) {
		if p == "/mnt/nfs" {
			return 0, 0, 0, "statfs timed out"
		}
		u := usage[p]
		return uint64(u[0]), uint64(u[1]), u[2], ""
	}
	got := buildMounts(entries, []RolePath{
		{RoleDockerRoot, "/var/lib/docker"},
		{RoleContainerdRoot, "/var/lib/containerd"},
		{RoleStacks, "/opt/stacks"},
		{RoleBackupTarget, "/mnt/nfs/dockmesh"},
		{RoleData, "/does/not/exist"},
	}, keyOf, use)

	want := []Mount{
		{MountPoint: "/var/lib/containerd", Device: "/dev/sdb1", FSType: "xfs", Total: 20, Used: 20, Percent: 100, Roles: []string{RoleContainerdRoot}},
		{MountPoint: "/", Device: "/dev/sda1", FSType: "ext4", Total: 100, Used: 5, Percent: 5, Roles: []string{RoleDockerRoot, RoleStacks}},
		{MountPoint: "/mnt/nfs", Device: "nas:/backups", FSType: "nfs4", Roles: []string{RoleBackupTarget}, Error: "statfs timed out"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildMounts =\n%+v\nwant\n%+v", got, want)
	}
}

func TestApplyMountsUsesFullestDockerMount(t *testing.T) {
	tests := []struct {
		name     string
		mounts   []Mount
		wantPath string
		wantPct  float64
	}{
		{
			name: "full containerd mount wins over data dir",
			mounts: []Mount{
				{MountPoint: "/backup", Total: 10, Used: 10, Percent: 100, Roles: []string{RoleBackupTarget}},
				{MountPoint: "/var/lib/containerd", Total: 20, Used: 19, Percent: 95, Roles: []string{RoleContainerdRoot}},
				{MountPoint: "/", Total: 50, Used: 5, Percent: 10, Roles: []string{RoleData}},
			},
			wantPath: "/var/lib/containerd",
			wantPct:  95,
		},
		{
			name:     "no labelled mount keeps the old figure",
			mounts:   []Mount{{MountPoint: "/home", Total: 10, Used: 9, Percent: 90}},
			wantPath: "/var/lib/docker",
			wantPct:  42,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ApplyMounts(Metrics{DiskPath: "/var/lib/docker", DiskPercent: 42}, tt.mounts)
			if m.DiskPath != tt.wantPath || m.DiskPercent != tt.wantPct {
				t.Fatalf("summary = %s %.0f%%, want %s %.0f%%", m.DiskPath, m.DiskPercent, tt.wantPath, tt.wantPct)
			}
			if len(m.Mounts) != len(tt.mounts) {
				t.Fatalf("mounts not attached")
			}
		})
	}
}

func TestParseContainerdRoot(t *testing.T) {
	tests := []struct {
		name, toml, want string
	}{
		{"empty", "", "/var/lib/containerd"},
		{"top-level root", "version = 2\nroot = \"/data/containerd\"\nstate = \"/run/containerd\"\n", "/data/containerd"},
		{"plugin root ignored", "version = 2\n[plugins.\"io.containerd.grpc.v1.cri\"]\n  root = \"/elsewhere\"\n", "/var/lib/containerd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseContainerdRoot([]byte(tt.toml)); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBoundedFSHungCallFailsFast(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	hung := func() int { <-release; return 1 }

	start := time.Now()
	if _, ok := boundedFS("test:hung", hung); ok {
		t.Fatal("hung call reported success")
	}
	if d := time.Since(start); d < statTimeout || d > statTimeout+time.Second {
		t.Fatalf("first call took %s, want about %s", d, statTimeout)
	}
	start = time.Now()
	if _, ok := boundedFS("test:hung", hung); ok || time.Since(start) > 100*time.Millisecond {
		t.Fatal("second call on a still-blocked key must fail immediately")
	}
	if v, ok := boundedFS("test:fast", func() int { return 7 }); !ok || v != 7 {
		t.Fatalf("fast call = %d,%v", v, ok)
	}
}
