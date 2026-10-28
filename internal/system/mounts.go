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
	MountPoint string `json:"mount_point"`
	// Device can name file servers and share paths; it stays server-side.
	Device  string   `json:"-"`
	FSType  string   `json:"fs_type"`
	Total   uint64   `json:"total"`
	Used    uint64   `json:"used"`
	Percent float64  `json:"percent"`
	Roles   []string `json:"roles,omitempty"`
	Error   string   `json:"error,omitempty"`
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

// statTimeout bounds each stat/statfs call: on a hung network mount
// they block in the kernel. A key whose previous call is still blocked
// fails fast instead of piling up another stuck goroutine.
const statTimeout = 2 * time.Second

var inflight sync.Map

func boundedFS[T any](key string, fn func() T) (T, bool) {
	var zero T
	if _, busy := inflight.LoadOrStore(key, struct{}{}); busy {
		return zero, false
	}
	ch := make(chan T, 1)
	go func() {
		defer inflight.Delete(key)
		ch <- fn()
	}()
	select {
	case v := <-ch:
		return v, true
	case <-time.After(statTimeout):
		return zero, false
	}
}

func usageWithTimeout(point string) (total, used uint64, percent float64, errMsg string) {
	type res struct {
		total, used uint64
		pct         float64
	}
	r, ok := boundedFS("statfs:"+point, func() res {
		t, u, p := readDisk(point)
		return res{t, u, p}
	})
	if !ok {
		return 0, 0, 0, "statfs timed out"
	}
	return r.total, r.used, r.pct, ""
}

// CollectMounts lists every real filesystem with its usage, labels the
// ones holding the given role paths, and sorts the fullest first.
func CollectMounts(paths []RolePath) []Mount {
	return buildMounts(listMounts(), paths, resolvedDeviceKey, usageWithTimeout)
}

func resolvedDeviceKey(p string) (string, bool) {
	type res struct {
		key string
		ok  bool
	}
	r, done := boundedFS("stat:"+p, func() res {
		q := p
		if abs, err := filepath.Abs(q); err == nil {
			q = abs
		}
		if real, err := filepath.EvalSymlinks(q); err == nil {
			q = real
		}
		k, ok := deviceKey(q)
		return res{k, ok}
	})
	return r.key, done && r.ok
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
	mountsDone  chan struct{} // non-nil while a refresh runs
)

// LocalMounts returns CollectMounts for this host, cached briefly since
// the dashboard polls it and role lookup asks the Docker daemon. The
// refresh runs in the background; callers get the previous result while
// it is in flight, so a slow mount never blocks a request for long.
func LocalMounts(ctx context.Context) []Mount {
	mountsMu.Lock()
	cached := mountsCache
	if cached != nil && time.Since(mountsAt) < mountsCacheTTL {
		mountsMu.Unlock()
		return cached
	}
	if mountsDone == nil {
		mountsDone = make(chan struct{})
		// Detached from ctx: one caller giving up must not cancel the refresh
		// the next callers are waiting for.
		go refreshMounts(mountsDone) //nolint:contextcheck
	}
	done := mountsDone
	mountsMu.Unlock()
	if cached != nil {
		return cached
	}
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
	mountsMu.Lock()
	defer mountsMu.Unlock()
	return mountsCache
}

func refreshMounts(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var paths []RolePath
	if fn := rolePathsFn.Load(); fn != nil {
		paths = (*fn)(ctx)
	}
	mounts := CollectMounts(paths)
	mountsMu.Lock()
	mountsCache = mounts
	mountsAt = time.Now()
	mountsDone = nil
	mountsMu.Unlock()
	close(done)
}

// CollectWithMounts is Collect plus the per-mount breakdown.
func CollectWithMounts(ctx context.Context) Metrics {
	// Collect reads the sampler snapshot and bounds its own Docker call.
	return ApplyMounts(Collect(), LocalMounts(ctx)) //nolint:contextcheck
}
