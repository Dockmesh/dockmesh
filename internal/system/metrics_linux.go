//go:build linux

package system

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Collect returns a fresh snapshot. Sleeps ~100ms for the CPU delta
// sample — callers should batch, not call per-frame.
func Collect() Metrics {
	m := Metrics{
		CPUCores: runtime.NumCPU(),
		DiskPath: "/var/lib/docker",
	}

	m.CPUPercent = readCPUPercent()
	m.CPUUsed = float64(m.CPUCores) * m.CPUPercent / 100.0
	m.MemTotal, m.MemUsed, m.MemPercent = readMem()

	path := "/var/lib/docker"
	if _, err := os.Stat(path); err != nil {
		path = "/"
	}
	m.DiskPath = path
	m.DiskTotal, m.DiskUsed, m.DiskPercent = readDisk(path)

	m.Uptime = readUptime()
	return m
}

func readCPUPercent() float64 {
	a, ok := readCPUTimes()
	if !ok {
		return 0
	}
	time.Sleep(100 * time.Millisecond)
	b, ok := readCPUTimes()
	if !ok {
		return 0
	}
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
