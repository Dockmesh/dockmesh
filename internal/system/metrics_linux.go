//go:build linux

package system

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Collect returns the latest cached snapshot from the background sampler.
// If the sampler hasn't produced a value yet (very early in startup) it
// falls back to a one-shot 100ms CPU delta so the API never returns
// zeros.
//
// The old behaviour was a 100ms-window sample taken on every request,
// which made the dashboard tiles jitter 15-20% between polls even on
// an idle server (a single kernel context-switch landing inside the
// 100ms window could swing the ratio by a few points). The sampler
// now runs a 500ms tick on a 5-second rolling window and returns the
// mean, so the number visually "breathes" instead of flickering.
func Collect() Metrics {
	samplerMu.RLock()
	ready := samplerReady
	snap := samplerSnap
	samplerMu.RUnlock()
	if ready {
		// Apply Docker resource limits to the cached sample — on a plain
		// Linux host Docker's NCPU == host NCPU so the override is a no-op,
		// but on cgroup-constrained or container-in-container setups the
		// daemon's view is the load-bearing one for the dashboard.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return applyDockerLimits(ctx, snap)
	}
	return collectOneShot()
}

// collectOneShot is the old behaviour — used as a warm-up fallback
// before the background sampler has filled its window.
func collectOneShot() Metrics {
	m := Metrics{
		CPUCores: runtime.NumCPU(),
		DiskPath: "/var/lib/docker",
	}
	m.CPUPercent = readCPUPercentOneShot()
	m.CPUUsed = float64(m.CPUCores) * m.CPUPercent / 100.0
	m.MemTotal, m.MemUsed, m.MemPercent = readMem()
	path := "/var/lib/docker"
	if _, err := os.Stat(path); err != nil {
		path = "/"
	}
	m.DiskPath = path
	m.DiskTotal, m.DiskUsed, m.DiskPercent = readDisk(path)
	m.Uptime = readUptime()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return applyDockerLimits(ctx, m)
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

// Background sampler state. Writers hold samplerMu.Lock, readers
// take RLock. Initialised by StartSampler at process boot.
var (
	samplerOnce  sync.Once
	samplerMu    sync.RWMutex
	samplerSnap  Metrics
	samplerReady bool
)

// StartSampler spins a goroutine that takes a CPU/mem/disk sample
// every 500ms and keeps a rolling mean over the last 5s. Called once
// from main(); subsequent calls are no-ops.
//
// Window size rationale: 5s / 500ms = 10 samples. Long enough to
// smooth a single context-switch spike, short enough that a real
// load change shows up on the dashboard within ~2 seconds of the
// next poll.
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
		// /proc/stat missing — leave sampler unready; Collect
		// will fall back to the one-shot path forever.
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
			path := "/var/lib/docker"
			if _, err := os.Stat(path); err != nil {
				path = "/"
			}
			m.DiskPath = path
			m.DiskTotal, m.DiskUsed, m.DiskPercent = readDisk(path)
			m.Uptime = readUptime()

			samplerMu.Lock()
			samplerSnap = m
			samplerReady = true
			samplerMu.Unlock()
		}
	}
}

type cpuTimes struct {
	total uint64
	idle  uint64
}

func readCPUTimes() (cpuTimes, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return cpuTimes{}, false
	}
	line := sc.Text()
	if !strings.HasPrefix(line, "cpu ") {
		return cpuTimes{}, false
	}
	parts := strings.Fields(line)[1:]
	var ct cpuTimes
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			continue
		}
		ct.total += n
		// Field 3 = idle, field 4 = iowait (also counted as idle).
		if i == 3 || i == 4 {
			ct.idle += n
		}
	}
	return ct, true
}

func readMem() (total, used uint64, percent float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()
	var memTotal, memAvail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			memTotal = parseKB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			memAvail = parseKB(line)
		}
		if memTotal > 0 && memAvail > 0 {
			break
		}
	}
	if memTotal == 0 {
		return 0, 0, 0
	}
	used = memTotal - memAvail
	percent = float64(used) / float64(memTotal) * 100.0
	return memTotal, used, percent
}

func parseKB(line string) uint64 {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	n, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0
	}
	return n * 1024
}

func readDisk(path string) (total, used uint64, percent float64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	used = total - free
	if total == 0 {
		return 0, 0, 0
	}
	percent = float64(used) / float64(total) * 100.0
	return total, used, percent
}

func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	parts := strings.Fields(string(b))
	if len(parts) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}
	return int64(f)
}
