package system

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Mount roles: what Docker or Dockmesh keeps on a filesystem.
const (
	RoleDockerRoot     = "docker_root"
	RoleContainerdRoot = "containerd_root"
	RoleStacks         = "stacks"
	RoleData           = "data"
	RoleBackupTarget   = "backup_target"
)

// dockerRelevant roles decide which mount stands in for the host's disk
// summary and which mounts get the default disk alert.
var dockerRelevant = map[string]bool{
	RoleDockerRoot:     true,
	RoleContainerdRoot: true,
	RoleStacks:         true,
	RoleData:           true,
}

// Mount is the usage of one real filesystem.
type Mount struct {
	MountPoint string   `json:"mount_point"`
	Device     string   `json:"device"`
	FSType     string   `json:"fs_type"`
	Total      uint64   `json:"total"`
	Used       uint64   `json:"used"`
	Percent    float64  `json:"percent"`
	Roles      []string `json:"roles,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// DockerRelevant reports whether m holds Docker or Dockmesh data.
func (m Mount) DockerRelevant() bool {
	for _, r := range m.Roles {
		if dockerRelevant[r] {
			return true
		}
	}
	return false
}

// RolePath names a directory whose filesystem gets the role label.
type RolePath struct {
	Role string
	Path string
}

// mountEntry is one row of the platform mount table. Key identifies the
// underlying filesystem so bind mounts of the same device collapse.
type mountEntry struct {
	point, device, fstype, key string
}

var virtualFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "overlay": true, "proc": true, "sysfs": true,
	"cgroup": true, "cgroup2": true, "squashfs": true, "nsfs": true, "devpts": true,
	"mqueue": true, "hugetlbfs": true, "debugfs": true, "tracefs": true,
	"securityfs": true, "pstore": true, "bpf": true, "configfs": true,
	"fusectl": true, "binfmt_misc": true, "autofs": true, "rpc_pipefs": true,
	"efivarfs": true, "ramfs": true, "selinuxfs": true, "fuse.lxcfs": true,
	"fuse.gvfsd-fuse": true, "fuse.portal": true, "shm": true, "devfs": true,
	"nullfs": true, "aufs": true,
}

// statTimeout bounds statfs on a hung network mount. A mount whose
// previous statfs is still blocked is reported with an error instead of
// starting another blocked call.
const statTimeout = 2 * time.Second

var inflight sync.Map

func usageWithTimeout(point string) (total, used uint64, percent float64, errMsg string) {
	if _, busy := inflight.LoadOrStore(point, struct{}{}); busy {
		return 0, 0, 0, "statfs still pending"
	}
	type res struct {
		total, used uint64
		pct         float64
	}
	ch := make(chan res, 1)
	go func() {
		defer inflight.Delete(point)
		t, u, p := readDisk(point)
		ch <- res{t, u, p}
	}()
	select {
	case r := <-ch:
		return r.total, r.used, r.pct, ""
	case <-time.After(statTimeout):
		return 0, 0, 0, "statfs timed out"
	}
}

// CollectMounts lists every real filesystem with its usage, labels the
// ones holding the given role paths, and sorts the fullest first.
func CollectMounts(paths []RolePath) []Mount {
	return buildMounts(listMounts(), paths, resolvedDeviceKey, usageWithTimeout)
}

func resolvedDeviceKey(p string) (string, bool) {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	return deviceKey(p)
}

func buildMounts(entries []mountEntry, paths []RolePath,
	keyOf func(string) (string, bool),
	usage func(string) (uint64, uint64, float64, string),
) []Mount {
	byKey := map[string]*Mount{}
	var keys []string
	for _, e := range entries {
		if virtualFS[e.fstype] {
			continue
		}
		if m, ok := byKey[e.key]; ok {
			if len(e.point) < len(m.MountPoint) {
				m.MountPoint = e.point
			}
			continue
		}
		byKey[e.key] = &Mount{MountPoint: e.point, Device: e.device, FSType: e.fstype}
		keys = append(keys, e.key)
	}

	for _, rp := range paths {
		if rp.Path == "" {
			continue
		}
		key, ok := keyOf(rp.Path)
		if !ok {
			continue
		}
		if m, ok := byKey[key]; ok && !hasRole(m.Roles, rp.Role) {
			m.Roles = append(m.Roles, rp.Role)
		}
	}

	out := make([]Mount, 0, len(keys))
	for _, k := range keys {
		m := byKey[k]
		m.Total, m.Used, m.Percent, m.Error = usage(m.MountPoint)
		if m.Total == 0 && m.Error == "" {
			continue
		}
		sort.Strings(m.Roles)
		out = append(out, *m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Percent > out[j].Percent })
	return out
}

func hasRole(roles []string, r string) bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

// ApplyMounts attaches mounts to m and replaces the single disk figure
// with the fullest Docker-relevant mount, so a full Docker or containerd
// filesystem shows up in the host summary.
func ApplyMounts(m Metrics, mounts []Mount) Metrics {
	m.Mounts = mounts
	for _, mt := range mounts {
		if mt.DockerRelevant() && mt.Total > 0 {
			m.DiskPath = mt.MountPoint
			m.DiskTotal = mt.Total
			m.DiskUsed = mt.Used
			m.DiskPercent = mt.Percent
			break
		}
	}
	return m
}

var (
	containerdRootRe = regexp.MustCompile(`(?m)^\s*root\s*=\s*"([^"]+)"`)
	tomlSectionRe    = regexp.MustCompile(`(?m)^\s*\[`)
)

const defaultContainerdRoot = "/var/lib/containerd"

// ContainerdRoot returns containerd's root directory: the top-level
// `root` of /etc/containerd/config.toml when readable, else the default.
func ContainerdRoot() string {
	b, err := os.ReadFile("/etc/containerd/config.toml")
	if err != nil {
		return defaultContainerdRoot
	}
	return parseContainerdRoot(b)
}

// parseContainerdRoot only looks above the first [section]: plugins
// have their own `root` keys.
func parseContainerdRoot(b []byte) string {
	if i := tomlSectionRe.FindIndex(b); i != nil {
		b = b[:i[0]]
	}
	if m := containerdRootRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return defaultContainerdRoot
}

// RolePathsFn returns the role paths of the host this process runs on.
type RolePathsFn func(ctx context.Context) []RolePath

var rolePathsFn atomic.Pointer[RolePathsFn]

// SetRolePathsFn wires the lookup of Docker root, stacks dir and so on.
func SetRolePathsFn(fn RolePathsFn) {
	if fn == nil {
		rolePathsFn.Store(nil)
		return
	}
	rolePathsFn.Store(&fn)
}

const mountsCacheTTL = 15 * time.Second

var (
	mountsMu    sync.Mutex
	mountsCache []Mount
	mountsAt    time.Time
)

// LocalMounts returns CollectMounts for this host, cached briefly since
// the dashboard polls it and role lookup asks the Docker daemon.
func LocalMounts(ctx context.Context) []Mount {
	mountsMu.Lock()
	defer mountsMu.Unlock()
	if mountsCache != nil && time.Since(mountsAt) < mountsCacheTTL {
		return mountsCache
	}
	var paths []RolePath
	if fn := rolePathsFn.Load(); fn != nil {
		paths = (*fn)(ctx)
	}
	mountsCache = CollectMounts(paths)
	mountsAt = time.Now()
	return mountsCache
}

// CollectWithMounts is Collect plus the per-mount breakdown.
func CollectWithMounts(ctx context.Context) Metrics {
	return ApplyMounts(Collect(), LocalMounts(ctx))
}
