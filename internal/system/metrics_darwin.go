//go:build darwin

// Native macOS metrics. Darwin doesn't expose FreeBSD-ish sysctl
// counters like `kern.cp_time`, and the real source of truth for CPU
// load is Mach's `host_statistics()` which requires either CGO or the
// mach syscall glue — neither of which dockmesh wants to drag in.
//
// Instead we shell out to Apple-maintained tools:
//   - `top -l 2 -s 1 -n 0` → 2 snapshots 1s apart. The second
//     snapshot's "CPU usage:" line gives us a valid 1-second-window
//     delta (the first is the since-boot average, useless). The
//     "PhysMem:" line gives us Apple's own Memory-Used calculation
//     matching Activity Monitor, including its handling of compressed
//     + reclaimable file-backed pages.
//   - `syscall.Statfs` on `/` for disk — Darwin BSD ancestry means it
//     works identically to Linux. We use `/` rather than
//     `/var/lib/docker` because Docker Desktop keeps its storage
//     inside a VM image that isn't visible from the host filesystem;
//     the root volume is what actually runs out first on a Mac.
//   - `sysctl -n kern.boottime` for uptime — this one DOES exist on
//     Darwin (just not cp_time).
package system

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func Collect() Metrics {
	samplerMu.RLock()
	ready := samplerReady
	snap := samplerSnap
	samplerMu.RUnlock()
	if ready {
		// Apply Docker limits on read rather than at sample time so a
		// transient docker-socket hiccup during sampling doesn't
		// permanently clear the HostCPUCores / HostMemTotal fields.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return applyDockerLimits(ctx, snap)
	}
	return collectOneShot()
}

// collectOneShot is used before the sampler's first successful tick
// finishes. Identical cost-profile to a sampler tick (runs `top -l 2
// -s 1`), so ~1s to the caller — but it's only hit in the first 10s
// after startup before the sampler has populated its snapshot.
func collectOneShot() Metrics {
	m := Metrics{CPUCores: runtime.NumCPU(), DiskPath: "/"}
	if cpu, memTotal, memUsed, ok := readTopSnapshot(); ok {
		m.CPUPercent = cpu
		m.CPUUsed = float64(m.CPUCores) * cpu / 100.0
		m.MemTotal = memTotal
		m.MemUsed = memUsed
		if memTotal > 0 {
			m.MemPercent = float64(memUsed) / float64(memTotal) * 100.0
		}
	}
	m.DiskTotal, m.DiskUsed, m.DiskPercent = readDisk("/")
	m.Uptime = readUptime()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return applyDockerLimits(ctx, m)
}

var (
	samplerOnce  sync.Once
	samplerMu    sync.RWMutex
	samplerSnap  Metrics
	samplerReady bool
)

func StartSampler(ctx context.Context) {
	samplerOnce.Do(func() {
		go runSampler(ctx)
	})
}

// runSampler polls every 10 seconds. `top` blocks for ~1s per call
// (due to the -s 1 interval between its two snapshots), so 10s means
// ~10% of one core used by the sampler tool. Faster polling would be
// visible as sustained background CPU; slower polling makes the
// dashboard stale. The web frontend refreshes /system/metrics every
// 10s anyway, so this cadence matches.
//
// Unlike the Linux sampler, there's no need for a rolling window:
// `top`'s own 1-second sample window already smooths single-spike
// noise. We cache the result and serve it directly.
func runSampler(ctx context.Context) {
	const tick = 10 * time.Second

	// Prime immediately so the dashboard's first poll after startup
	// doesn't hit collectOneShot's synchronous 1-second stall.
	update(ctx)

	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update(ctx)
		}
	}
}

func update(ctx context.Context) {
	cpu, memTotal, memUsed, ok := readTopSnapshot()
	if !ok {
		return
	}
	m := Metrics{CPUCores: runtime.NumCPU(), DiskPath: "/"}
	m.CPUPercent = cpu
	m.CPUUsed = float64(m.CPUCores) * cpu / 100.0
	m.MemTotal = memTotal
	m.MemUsed = memUsed
	if memTotal > 0 {
		m.MemPercent = float64(memUsed) / float64(memTotal) * 100.0
	}
	m.DiskTotal, m.DiskUsed, m.DiskPercent = readDisk("/")
	m.Uptime = readUptime()

	samplerMu.Lock()
	samplerSnap = m
	samplerReady = true
	samplerMu.Unlock()
	_ = ctx
}

// readTopSnapshot runs `top -l 2 -s 1 -n 0` and parses the last
// CPU-usage + PhysMem lines. Returns cpu% (user+sys), mem_total,
// mem_used. The second snapshot is the authoritative one — the first
// is the since-boot average and would make the dashboard show a
// meaningless long-term number.
func readTopSnapshot() (cpu float64, memTotal, memUsed uint64, ok bool) {
	out, err := exec.Command("top", "-l", "2", "-s", "1", "-n", "0").Output()
	if err != nil {
		return 0, 0, 0, false
	}
	var lastCPU, lastMem string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "CPU usage:") {
			lastCPU = line
		} else if strings.HasPrefix(line, "PhysMem:") {
			lastMem = line
		}
	}
	if lastCPU == "" || lastMem == "" {
		return 0, 0, 0, false
	}

	// "CPU usage: 3.45% user, 1.22% sys, 95.33% idle"
	var user, sys, idle float64
	if _, err := fmt.Sscanf(lastCPU, "CPU usage: %f%% user, %f%% sys, %f%% idle",
		&user, &sys, &idle); err != nil {
		// Some locales / versions may swap order or add whitespace;
		// fall back to regex-ish token walk.
		user, sys = parseCPULineTokens(lastCPU)
	}
	cpu = user + sys
	if cpu < 0 {
		cpu = 0
	}
	if cpu > 100 {
		cpu = 100
	}

	// "PhysMem: 11G used (3010M wired, 1234M compressor), 4814M unused."
	usedB, unusedB := parsePhysMemLine(lastMem)
	if usedB == 0 && unusedB == 0 {
		return cpu, 0, 0, true // CPU still valid even if mem parse fails
	}
	memTotal = usedB + unusedB
	memUsed = usedB
	return cpu, memTotal, memUsed, true
}

// parseCPULineTokens is a defensive fallback for the Sscanf path —
// scan the line for "X.X% user" / "X.X% sys" token pairs without
// requiring the exact known phrase order.
func parseCPULineTokens(line string) (user, sys float64) {
	fields := strings.Fields(line)
	for i := 0; i < len(fields)-1; i++ {
		next := strings.TrimSuffix(fields[i+1], ",")
		pct := strings.TrimSuffix(fields[i], "%")
		v, err := strconv.ParseFloat(pct, 64)
		if err != nil {
			continue
		}
		switch next {
		case "user":
			user = v
		case "sys":
			sys = v
		}
	}
	return
}

// parsePhysMemLine extracts the two byte-counts immediately preceding
// "used" and "unused" in top's "PhysMem:" line. Tolerates the parenthesised
// breakdown ("(3010M wired, 1234M compressor)") by just looking for the
// keyword positions.
func parsePhysMemLine(line string) (used, unused uint64) {
	fields := strings.Fields(line)
	for i, f := range fields {
		switch strings.TrimSuffix(f, ".") {
		case "used":
			if i > 0 {
				used = parseHumanBytes(fields[i-1])
			}
		case "unused":
			if i > 0 {
				unused = parseHumanBytes(fields[i-1])
			}
		}
	}
	return
}

// parseHumanBytes converts top's human-readable sizes like "11G",
// "4814M", "512K" to raw bytes. Unit characters: B (bytes), K, M, G,
// T. Accepts optional trailing period ("4814M.") which top sometimes
// leaves when the token was line-terminal.
func parseHumanBytes(s string) uint64 {
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		return 0
	}
	unit := s[len(s)-1]
	var mul uint64
	switch unit {
	case 'K', 'k':
		mul = 1024
	case 'M', 'm':
		mul = 1024 * 1024
	case 'G', 'g':
		mul = 1024 * 1024 * 1024
	case 'T', 't':
		mul = 1024 * 1024 * 1024 * 1024
	case 'B', 'b':
		mul = 1
	default:
		// No unit suffix — treat whole string as raw bytes.
		if v, err := strconv.ParseUint(s, 10, 64); err == nil {
			return v
		}
		return 0
	}
	num := s[:len(s)-1]
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	return uint64(n * float64(mul))
}

// readDisk uses syscall.Statfs which is available on Darwin with the
// same semantics as Linux (BSD ancestry). Bavail avoids double-counting
// root-reserved blocks a regular process can't touch.
func readDisk(path string) (total, used uint64, percent float64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if total == 0 {
		return 0, 0, 0
	}
	if free > total {
		free = total
	}
	used = total - free
	percent = float64(used) / float64(total) * 100.0
	return total, used, percent
}

// readUptime parses `sysctl -n kern.boottime` which on macOS returns
// "{ sec = 1700000000, usec = 0 } Tue Nov 14 12:13:20 2023" — we just
// need the sec value. This sysctl node DOES exist on Darwin (unlike
// kern.cp_time).
func readUptime() int64 {
	out, err := exec.Command("sysctl", "-n", "kern.boottime").Output()
	if err != nil {
		return 0
	}
	s := string(out)
	idx := strings.Index(s, "sec = ")
	if idx < 0 {
		return 0
	}
	rest := s[idx+len("sec = "):]
	end := strings.IndexAny(rest, ",}")
	if end < 0 {
		return 0
	}
	boot, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return time.Now().Unix() - boot
}
