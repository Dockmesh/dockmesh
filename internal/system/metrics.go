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
	// operators can compare "Docker is saturated" against "host has
	// plenty of headroom — maybe raise the allocation". Omitted from
	// JSON when identical to the active values to keep the payload
	// small on Linux where they always match.
	HostCPUPercent float64 `json:"host_cpu_percent,omitempty"`
	HostCPUCores   int     `json:"host_cpu_cores,omitempty"`
	HostCPUUsed    float64 `json:"host_cpu_used_cores,omitempty"`
	HostMemPercent float64 `json:"host_mem_percent,omitempty"`
	HostMemTotal   uint64  `json:"host_mem_total,omitempty"`
	HostMemUsed    uint64  `json:"host_mem_used,omitempty"`
}

// DockerSnapshot is Docker's own view of the resources it's using
// right now. Populated by the main-loop's DockerInfoFn via
// `docker info` + per-container stats aggregation.
//
// Why we can't just use host metrics on macOS/Windows: Docker Desktop
// runs in a VM that's invisible to the host. host `top` measures the
// whole Mac (macOS + Chrome + Slack + Docker), which has no meaningful
// relationship to what's happening inside the Docker VM. If the
// operator capped Docker at 6 cores + 8 GB, they want to see "how
// utilised is that allocation right now" — which means summing
// container stats inside Docker.
type DockerSnapshot struct {
	NCPU     int
	MemTotal uint64
	// MemUsed / CPUPercent are 0 when the info fn couldn't aggregate
	// container stats (daemon slow, timed out, returned no containers).
	// In that case applyDockerLimits falls back to the host-measured
	// values so the tile at least shows *something* sensible.
	MemUsed    uint64
	CPUPercent float64
}

// DockerInfoFn is called by platform-specific samplers to fetch
// Docker's own resource-limit view + its actual current usage.
// Returns (snapshot, true) when the daemon responds. Set once at
// startup via SetDockerInfoFn.
type DockerInfoFn func(ctx context.Context) (DockerSnapshot, bool)

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
	snap, ok := (*fn)(ctx)
	if !ok || snap.NCPU <= 0 || snap.MemTotal == 0 {
		return m
	}
	// Skip the override if Docker's totals match what we already have —
	// avoids flagging "limited" on a plain Linux host where Docker's
	// NCPU / MemTotal legitimately equal the host hardware. The
	// dashboard stays simple for that 95% case.
	if snap.NCPU == m.CPUCores && snap.MemTotal == m.MemTotal {
		return m
	}
	// Save the host-measured values so the UI can render "Docker 1.2/8
	// GB used" on the primary line and "Host 30/64 GB used" below.
	m.HostCPUPercent = m.CPUPercent
	m.HostCPUCores = m.CPUCores
	m.HostCPUUsed = m.CPUUsed
	m.HostMemPercent = m.MemPercent
	m.HostMemTotal = m.MemTotal
	m.HostMemUsed = m.MemUsed

	m.CPUCores = snap.NCPU
	m.MemTotal = snap.MemTotal

	// CPU + Memory need care: host-measured values (macOS `top` + `PhysMem`)
	// reflect the WHOLE Mac (macOS + all apps + Docker VM as one process),
	// which has no meaningful relationship to what's happening inside the
	// Docker VM. Capping host-used to Docker-total produces the garbage
	// "8 GB of 8 GB always" tile we shipped in v0.2.6.
	//
	// If the caller aggregated container stats for us, use those — that's
	// the authoritative answer for "how much of my Docker allocation is
	// actually in use". Else fall back to host values (close approximation
	// for CPU when Docker is the dominant workload, poor approximation
	// for memory).
	if snap.CPUPercent > 0 {
		m.CPUPercent = snap.CPUPercent
	}
	m.CPUUsed = float64(m.CPUCores) * m.CPUPercent / 100.0

	if snap.MemUsed > 0 {
		m.MemUsed = snap.MemUsed
	}
	if m.MemTotal > 0 {
		m.MemPercent = float64(m.MemUsed) / float64(m.MemTotal) * 100.0
	}
	m.DockerLimited = true
	return m
}
