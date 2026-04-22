//go:build darwin

// Native macOS metrics. Uses `sysctl` + `vm_stat` via os/exec (no CGO,
// no external deps) and syscall.Statfs for disk. Parity with the Linux
// sampler: background goroutine takes a sample every 500ms and keeps
// a 5-second rolling mean so dashboard tiles don't jitter on single
// context-switch noise.
//
// Why not /var/lib/docker for the disk tile? On macOS Docker Desktop
// keeps its storage inside a VM image (`Docker.raw` under
// ~/Library/Containers/com.docker.docker/Data) that isn't visible
// from the host filesystem. What the operator actually cares about
// on a Mac is "is my root volume full?" — so we report `/`.
package system

import (
	"bufio"
	"context"
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
		return snap
	}
	return collectOneShot()
}

func collectOneShot() Metrics {
	m := Metrics{CPUCores: runtime.NumCPU()}
	m.CPUPercent = readCPUPercentOneShot()
	m.CPUUsed = float64(m.CPUCores) * m.CPUPercent / 100.0
	m.MemTotal, m.MemUsed, m.MemPercent = readMem()
	path := "/"
	m.DiskPath = path
	m.DiskTotal, m.DiskUsed, m.DiskPercent = readDisk(path)
	m.Uptime = readUptime()
	return m
}

func readCPUPercentOneShot() float64 {
	a, ok := readCPUTimes()
	if !ok {
		return 0
	}
	time.Sleep(100 * time.Millisecond)
	b, ok := readCPUTimes()
	if !ok {
		return 0
	}
	return cpuPct(a, b)
}

func cpuPct(a, b cpuTimes) float64 {
	totalDelta := float64(b.total - a.total)
	idleDelta := float64(b.idle - a.idle)
	if totalDelta <= 0 {
		return 0
	}
	pct := (1.0 - idleDelta/totalDelta) * 100.0
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
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

func runSampler(ctx context.Context) {
	const (
		tick       = 500 * time.Millisecond
		windowSize = 10
	)
	samples := make([]float64, 0, windowSize)

	prev, ok := readCPUTimes()
	if !ok {
		return
	}

	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur, ok := readCPUTimes()
			if !ok {
				continue
			}
			pct := cpuPct(prev, cur)
			prev = cur
			samples = append(samples, pct)
			if len(samples) > windowSize {
				samples = samples[len(samples)-windowSize:]
			}
			var sum float64
			for _, s := range samples {
				sum += s
			}
			avg := sum / float64(len(samples))

			m := Metrics{CPUCores: runtime.NumCPU()}
			m.CPUPercent = avg
			m.CPUUsed = float64(m.CPUCores) * avg / 100.0
			m.MemTotal, m.MemUsed, m.MemPercent = readMem()
			m.DiskPath = "/"
			m.DiskTotal, m.DiskUsed, m.DiskPercent = readDisk("/")
			m.Uptime = readUptime()

			samplerMu.Lock()
			samplerSnap = m
			samplerReady = true
			samplerMu.Unlock()
		}
	}
}

// cpuTimes mirrors the Linux shape: total + idle ticks. `sysctl -n
// kern.cp_time` returns `user nice sys intr idle` as space-separated
// uint64, same semantics as /proc/stat's cpu line.
type cpuTimes struct {
	total uint64
	idle  uint64
}

func readCPUTimes() (cpuTimes, bool) {
	out, err := exec.Command("sysctl", "-n", "kern.cp_time").Output()
	if err != nil {
		return cpuTimes{}, false
	}
	parts := strings.Fields(string(out))
	if len(parts) < 5 {
		return cpuTimes{}, false
	}
	var ct cpuTimes
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			continue
		}
		ct.total += n
		if i == 4 { // index 4 == idle
			ct.idle += n
		}
	}
	return ct, true
}

// readMem derives a "used memory" number that matches what Activity
// Monitor shows. On macOS "free" is misleading because the kernel
// aggressively caches file-backed pages as "inactive" — those are
// reclaimable and shouldn't count as used.
//
// Formula used by Activity Monitor: used = wired + active + compressed
// total = sysctl hw.memsize
// free  = total - used
//
// We parse vm_stat's page counts and the detected page size from its
// header line (usually 4096 or 16384 on Apple Silicon).
func readMem() (total, used uint64, percent float64) {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, 0, 0
	}
	total, err = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || total == 0 {
		return 0, 0, 0
	}

	vm, err := exec.Command("vm_stat").Output()
	if err != nil {
		return total, 0, 0
	}
	pageSize := uint64(4096)
	var wired, active, compressed uint64
	sc := bufio.NewScanner(strings.NewReader(string(vm)))
	for sc.Scan() {
		line := sc.Text()
		// Header: "Mach Virtual Memory Statistics: (page size of 16384 bytes)"
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			if idx := strings.Index(line, "page size of "); idx >= 0 {
				rest := line[idx+len("page size of "):]
				if end := strings.Index(rest, " bytes"); end > 0 {
					if n, e := strconv.ParseUint(rest[:end], 10, 64); e == nil {
						pageSize = n
					}
				}
			}
			continue
		}
		name, val, ok := parseVMStatLine(line)
		if !ok {
			continue
		}
		switch name {
		case "Pages wired down":
			wired = val
		case "Pages active":
			active = val
		case "Pages occupied by compressor":
			compressed = val
		}
	}
	used = (wired + active + compressed) * pageSize
	if used > total {
		used = total
	}
	percent = float64(used) / float64(total) * 100.0
	return total, used, percent
}

func parseVMStatLine(line string) (name string, val uint64, ok bool) {
	// Lines are "Pages wired down:                          123456."
	// — colon separator, trailing period on the value.
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", 0, false
	}
	name = strings.TrimSpace(line[:idx])
	v := strings.TrimSpace(line[idx+1:])
	v = strings.TrimSuffix(v, ".")
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return "", 0, false
	}
	return name, n, true
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
// something like "{ sec = 1700000000, usec = 0 } Tue Nov 14 12:13:20 2023"
// — we just need the sec value.
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
