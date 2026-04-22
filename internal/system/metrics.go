// Package system reads host-level metrics (CPU / memory / disk) for the
// dashboard's "System health" panel. Linux is the production target;
// macOS has its own native implementation (metrics_darwin.go) that
// shells out to `top`/`sysctl`; truly unsupported platforms return
// zeros via metrics_other.go so the frontend doesn't crash.
//
// Docker resource limits override: if the operator has configured
// Docker Desktop (macOS/Windows) with explicit CPU + memory caps
// under Settings → Resources, the dashboard should reflect those
// numbers rather than the host's raw hardware. SetDockerInfoFn plugs
// the running docker client into the sampler so CPUCores + MemTotal
// can be replaced with Docker's own view when it's reachable.
package system

import (
	"context"
	"sync/atomic"
)

// Metrics captures a point-in-time snapshot of host load. All percentages
// are 0..100, all byte counts are raw bytes so the frontend can format
// them however it wants (GiB, GB, etc).
type Metrics struct {
	CPUPercent  float64 `json:"cpu_percent"`
	CPUCores    int     `json:"cpu_cores"`
	CPUUsed     float64 `json:"cpu_used_cores"` // fractional cores in use
	MemPercent  float64 `json:"mem_percent"`
	MemTotal    uint64  `json:"mem_total"`
	MemUsed     uint64  `json:"mem_used"`
	DiskPercent float64 `json:"disk_percent"`
	DiskTotal   uint64  `json:"disk_total"`
	DiskUsed    uint64  `json:"disk_used"`
	DiskPath    string  `json:"disk_path"`
	Uptime      int64   `json:"uptime_seconds"`

	// DockerLimited signals that CPUCores / MemTotal reflect Docker's
	// configured resource limits (Docker Desktop VM caps on macOS/Windows,
	// cgroup limits on constrained Linux hosts) rather than raw host
	// hardware. The UI uses this to show BOTH the Docker allocation
	// and the host hardware alongside.
	DockerLimited bool `json:"docker_limited,omitempty"`

	// Host-raw fields, populated only when they differ from the active
	// (Docker-limit-aware) view above. The dashboard renders both so
	// operators can compare "8 of 8 GB Docker = 100% used" against
	// "host has 64 GB sitting mostly idle — maybe raise the Docker
	// allocation". Omitted from JSON when identical to the active
	// values to keep the payload small on Linux where they always match.
	HostCPUCores int    `json:"host_cpu_cores,omitempty"`
	HostMemTotal uint64 `json:"host_mem_total,omitempty"`
}

// DockerInfoFn is called by platform-specific samplers to fetch
// Docker's own resource-limit view. Returns (ncpu, memTotal, true)
// when the daemon responds. Set once at startup via SetDockerInfoFn.
type DockerInfoFn func(ctx context.Context) (ncpu int, memTotal uint64, ok bool)

var dockerInfoFn atomic.Pointer[DockerInfoFn]

// SetDockerInfoFn wires a lookup function that the samplers use to
// override CPUCores + MemTotal with Docker's configured limits.
// Called once from main() after the docker.Client is up. Safe to call
// with nil to unset.
func SetDockerInfoFn(fn DockerInfoFn) {
	if fn == nil {
		dockerInfoFn.Store(nil)
		return
	}
	dockerInfoFn.Store(&fn)
}

// applyDockerLimits overrides CPUCores + MemTotal with Docker's view
// when the info function is wired up and the daemon is responsive.
// The host-raw values are preserved in HostCPUCores / HostMemTotal so
// the UI can show BOTH — Docker allocation vs actual hardware — when
// they diverge (Docker Desktop on a 64 GB Mac configured to hand only
// 8 GB to containers, etc.).
//
// When the Docker daemon is unreachable (no info-fn, timeout, error),
// we fall through silently and keep the host-derived values as the
// active view. Callers should pass a short-timeout ctx; 2s is the usual.
//
// Percent + Used fields are re-derived against the new totals because
// "35% of 6 cores" and "35% of 16 cores" are legitimately different
// numbers — the tile should reflect utilisation of the cap that
// actually matters for workloads (Docker's).
func applyDockerLimits(ctx context.Context, m Metrics) Metrics {
	fn := dockerInfoFn.Load()
	if fn == nil {
		return m
	}
	cpus, memTotal, ok := (*fn)(ctx)
	if !ok || cpus <= 0 || memTotal == 0 {
		return m
	}
	// Skip the override if Docker's numbers match what we already have —
	// avoids flagging "limited" on a plain Linux host where Docker's
	// NCPU / MemTotal legitimately equal the host hardware. The
	// dashboard stays simple for that 95% case.
	if cpus == m.CPUCores && memTotal == m.MemTotal {
		return m
	}
	m.HostCPUCores = m.CPUCores
	m.HostMemTotal = m.MemTotal
	m.CPUCores = cpus
	m.MemTotal = memTotal
	m.CPUUsed = float64(cpus) * m.CPUPercent / 100.0
	if m.MemUsed > memTotal {
		m.MemUsed = memTotal
	}
	if memTotal > 0 {
		m.MemPercent = float64(m.MemUsed) / float64(memTotal) * 100.0
	}
	m.DockerLimited = true
	return m
}
