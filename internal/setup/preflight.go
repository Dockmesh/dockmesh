package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
)

// Preflight collects host-level facts the wizard's first page renders.
// Each row carries a status so the UI can colour-code without further
// logic. Anything that's a soft warning ("Docker is on 19.x, recommend
// 20.10+") gets `Status = "warn"` plus a `Hint` link to docs; outright
// blockers ("Docker socket unreachable") are `Status = "fail"` and the
// wizard's Continue button stays disabled while any fail is present.
type Preflight struct {
	OK     bool             `json:"ok"`
	Checks []PreflightCheck `json:"checks"`
}

type PreflightCheck struct {
	Key     string `json:"key"`     // stable identifier for the UI to map labels
	Label   string `json:"label"`   // human-readable label, English
	Value   string `json:"value"`   // measured value, freeform string
	Status  string `json:"status"`  // "ok" | "warn" | "fail"
	Message string `json:"message,omitempty"`
	Hint    string `json:"hint,omitempty"` // docs link or short next-step
}

// CollectPreflight runs every host check the wizard's first page needs
// and returns a single Preflight snapshot. Per-check errors don't fail
// the call — they show up as `fail` rows so the operator sees what's
// missing rather than a generic 500.
func CollectPreflight(ctx context.Context, dockerCli *docker.Client) Preflight {
	pf := Preflight{OK: true}

	pf.add(checkOS())
	pf.add(checkCPU())
	pf.add(checkRAM())
	pf.add(checkDiskRoot())
	pf.add(checkDocker(ctx, dockerCli))
	pf.add(checkDockerSocket(dockerCli))
	pf.add(checkNTP())

	for _, c := range pf.Checks {
		if c.Status == "fail" {
			pf.OK = false
			break
		}
	}
	return pf
}

func (pf *Preflight) add(c PreflightCheck) {
	pf.Checks = append(pf.Checks, c)
}

// ---------------------------------------------------------------------------
// individual checks
// ---------------------------------------------------------------------------

func checkOS() PreflightCheck {
	c := PreflightCheck{Key: "os", Label: "Operating system", Status: "ok"}
	if runtime.GOOS != "linux" {
		c.Status = "warn"
		c.Value = runtime.GOOS + " " + runtime.GOARCH
		c.Message = "Dockmesh is primarily tested on Linux."
		return c
	}
	name, version := parseOSRelease()
	kernel := strings.TrimSpace(unameR())
	c.Value = fmt.Sprintf("%s %s · kernel %s · %s", name, version, kernel, runtime.GOARCH)
	return c
}

func checkCPU() PreflightCheck {
	c := PreflightCheck{Key: "cpu", Label: "CPU", Status: "ok"}
	cores := runtime.NumCPU()
	model := readCPUModel()
	if model != "" {
		c.Value = fmt.Sprintf("%d cores · %s", cores, model)
	} else {
		c.Value = fmt.Sprintf("%d cores", cores)
	}
	if cores < 1 {
		c.Status = "fail"
		c.Message = "no CPUs detected"
	}
	return c
}

func checkRAM() PreflightCheck {
	c := PreflightCheck{Key: "ram", Label: "RAM", Status: "ok"}
	total, free, err := readMeminfo()
	if err != nil {
		c.Status = "warn"
		c.Value = "—"
		c.Message = "could not read /proc/meminfo: " + err.Error()
		return c
	}
	c.Value = fmt.Sprintf("%s total · %s free", humanBytes(total), humanBytes(free))
	if total < 512*1024*1024 { // < 512 MiB
		c.Status = "warn"
		c.Message = "Dockmesh runs at ~30 MB baseline; <512 MB total leaves no headroom for Docker + your stacks."
	}
	return c
}

func checkDiskRoot() PreflightCheck {
	c := PreflightCheck{Key: "disk_root", Label: "Disk (root partition)", Status: "ok"}
	total, free, err := diskUsage("/")
	if err != nil {
		c.Status = "warn"
		c.Value = "—"
		c.Message = "could not stat /: " + err.Error()
		return c
	}
	c.Value = fmt.Sprintf("%s total · %s free", humanBytes(total), humanBytes(free))
	if free < 2*1024*1024*1024 { // < 2 GiB free
		c.Status = "warn"
		c.Message = "Less than 2 GB free on the root partition — Docker images alone often need this."
	}
	return c
}

func checkDocker(ctx context.Context, dc *docker.Client) PreflightCheck {
	c := PreflightCheck{Key: "docker_engine", Label: "Docker Engine", Status: "ok"}
	if dc == nil || !dc.Connected() {
		c.Status = "fail"
		c.Value = "not reachable"
		c.Message = "Dockmesh cannot run without a working Docker daemon."
		c.Hint = "https://docs.docker.com/engine/install/"
		return c
	}
	v, err := dc.Raw().ServerVersion(ctx)
	if err != nil {
		c.Status = "fail"
		c.Value = "error"
		c.Message = "ServerVersion failed: " + err.Error()
		return c
	}
	c.Value = fmt.Sprintf("%s · API %s", v.Version, v.APIVersion)
	// Soft-warn anything older than 20.10 — that's where the modern
	// compose plugin + APIv1.41 features Dockmesh relies on landed.
	if compareVersion(v.Version, "20.10") < 0 {
		c.Status = "warn"
		c.Message = "Dockmesh recommends Docker Engine 20.10 or newer."
		c.Hint = "https://docs.docker.com/engine/install/"
	}
	return c
}

func checkDockerSocket(dc *docker.Client) PreflightCheck {
	c := PreflightCheck{Key: "docker_socket", Label: "Docker daemon socket", Status: "ok"}
	if dc == nil || !dc.Connected() {
		c.Status = "fail"
		c.Value = "/var/run/docker.sock"
		c.Message = "Socket not reachable. Ensure the dockmesh service user is in the 'docker' group."
		return c
	}
	c.Value = "/var/run/docker.sock reachable"
	return c
}

func checkNTP() PreflightCheck {
	c := PreflightCheck{Key: "ntp", Label: "Time synchronisation", Status: "ok"}
	if runtime.GOOS != "linux" {
		c.Status = "warn"
		c.Value = "skipped (non-linux)"
		return c
	}
	out, err := exec.Command("timedatectl", "show", "--property=NTPSynchronized,NTP", "--value").Output()
	if err != nil {
		c.Status = "warn"
		c.Value = "unknown"
		c.Message = "timedatectl unavailable; mTLS handshakes are sensitive to clock skew."
		return c
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	synced := false
	for _, l := range lines {
		if strings.EqualFold(l, "yes") {
			synced = true
		}
	}
	if synced {
		c.Value = "synced"
	} else {
		c.Status = "warn"
		c.Value = "not synced"
		c.Message = "Clock not synced. Agent mTLS handshakes will fail until the host's time matches the server's."
	}
	return c
}

// ---------------------------------------------------------------------------
// helpers — small, no external deps
// ---------------------------------------------------------------------------

func parseOSRelease() (name, version string) {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "linux", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			name = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		} else if strings.HasPrefix(line, "VERSION_ID=") {
			version = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), `"`)
		}
	}
	if name == "" {
		name = "Linux"
	}
	return name, version
}

func unameR() string {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func readCPUModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "model name") {
			parts := strings.SplitN(l, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func readMeminfo() (total, free int64, err error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "MemTotal:"):
			total = parseKb(l)
		case strings.HasPrefix(l, "MemAvailable:"):
			free = parseKb(l)
		}
	}
	if total == 0 {
		return 0, 0, errors.New("MemTotal not found")
	}
	return total, free, nil
}

func parseKb(line string) int64 {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.ParseInt(parts[1], 10, 64)
	return n * 1024 // kB → bytes
}

// diskUsage is implemented per-OS — the unix variant uses statfs(2),
// the windows fallback returns "unsupported" so dev cross-compiles
// from Windows still work. The wizard always runs on Linux servers.

// humanBytes formats a byte count as e.g. "415 GB" / "7.7 GB" / "812 MB".
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	const unit = 1024.0
	v := float64(n) / unit
	suffixes := []string{"KB", "MB", "GB", "TB", "PB"}
	idx := 0
	for v >= unit && idx < len(suffixes)-1 {
		v /= unit
		idx++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, suffixes[idx])
	}
	return fmt.Sprintf("%.1f %s", v, suffixes[idx])
}

// compareVersion returns -1/0/+1 comparing dotted-int prefixes.
// "29.4.2" vs "20.10" → +1; "19.03.5" vs "20.10" → -1.
func compareVersion(a, b string) int {
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		ai, _ := strconv.Atoi(ap[i])
		bi, _ := strconv.Atoi(bp[i])
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// ServerInfo is the small JSON the wizard's top bar renders. Refreshed
// every ~10s by the wizard frontend so things like uptime stay live.
// Matches the user's request: version · os · ip · docker.
type ServerInfo struct {
	Version       string `json:"version"`
	OS            string `json:"os"`
	IP            string `json:"ip"`
	DockerVersion string `json:"docker_version"`
	UptimeSecs    int64  `json:"uptime_secs"`
}

// CollectServerInfo gathers the four-row top-bar payload.
func CollectServerInfo(ctx context.Context, version string, dockerCli *docker.Client, requestHost string, startedAt time.Time) ServerInfo {
	si := ServerInfo{Version: version}
	name, ver := parseOSRelease()
	if ver != "" {
		si.OS = name + " " + ver
	} else {
		si.OS = name
	}
	si.IP = requestHost
	si.UptimeSecs = int64(time.Since(startedAt).Seconds())
	if dockerCli != nil && dockerCli.Connected() {
		if v, err := dockerCli.Raw().ServerVersion(ctx); err == nil {
			si.DockerVersion = v.Version
		}
	}
	return si
}
