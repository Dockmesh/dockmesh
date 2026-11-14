package migration

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/bits"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"

	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/system"
)

// ContainerUsage is the part of the metrics collector the capacity check
// reads measured usage from.
type ContainerUsage interface {
	LatestPerContainer(ctx context.Context, hostID string, maxAgeSec int64, cores int) ([]metrics.Latest, error)
}

// SetUsage lets the preflight fall back to the measured usage of a
// stack's containers when its compose file declares no resources.
func (s *Service) SetUsage(u ContainerUsage) { s.usage = u }

// usageMaxAge is how old a container sample may be to count as current.
const usageMaxAge = 300

// stackNeeds is what a stack takes on the host it runs on. Each service
// counts with its declared reservation, else its limit, else what its
// containers on the source use now.
type stackNeeds struct {
	MemBytes uint64
	CPUs     float64
	// DiskBytes is the size of the images the target still has to pull.
	DiskBytes uint64

	// Services without a declaration or a current sample.
	MemUnknown, CPUUnknown []string
	// Images to pull whose size the source doesn't know.
	DiskUnknown []string
	Measured    bool
}

// serviceUsage is the measured usage of one service's containers.
type serviceUsage struct {
	containers int
	sampled    int
	mem        uint64
	cpus       float64
}

func computeNeeds(proj *types.Project, usage map[string]serviceUsage, srcImages, tgtImages []image.Summary) stackNeeds {
	var n stackNeeds
	names := make([]string, 0, len(proj.Services))
	for name := range proj.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	srcSize := map[string]int64{}
	for _, img := range srcImages {
		for _, tag := range img.RepoTags {
			srcSize[tag] = img.Size
		}
	}
	onTarget := map[string]bool{}
	for _, img := range tgtImages {
		for _, tag := range img.RepoTags {
			onTarget[tag] = true
		}
	}
	pulled := map[string]bool{}

	for _, name := range names {
		svc := proj.Services[name]
		u := usage[name]
		replicas := u.containers
		if replicas == 0 {
			replicas = svc.GetScale()
		}
		if replicas < 0 {
			replicas = 0
		}

		switch mem := declaredMem(svc); {
		case mem > 0:
			n.MemBytes = addSat(n.MemBytes, mulSat(mem, uint64(replicas))) //nolint:gosec // replicas >= 0
		case u.sampled > 0 && u.sampled == u.containers:
			n.MemBytes = addSat(n.MemBytes, u.mem)
			n.Measured = true
		case replicas > 0:
			n.MemUnknown = append(n.MemUnknown, name)
		}
		switch cpus := declaredCPUs(svc); {
		case cpus > 0:
			n.CPUs += cpus * float64(replicas)
		case u.sampled > 0 && u.sampled == u.containers:
			n.CPUs += u.cpus
			n.Measured = true
		case replicas > 0:
			n.CPUUnknown = append(n.CPUUnknown, name)
		}

		if svc.Image == "" || onTarget[svc.Image] || pulled[svc.Image] {
			continue
		}
		pulled[svc.Image] = true
		if size, ok := srcSize[svc.Image]; ok && size > 0 {
			n.DiskBytes = addSat(n.DiskBytes, uint64(size))
		} else {
			n.DiskUnknown = append(n.DiskUnknown, svc.Image)
		}
	}
	return n
}

func declaredMem(svc types.ServiceConfig) uint64 {
	var res *types.Resources
	if svc.Deploy != nil {
		res = &svc.Deploy.Resources
	}
	for _, v := range []types.UnitBytes{
		resMem(res, true), svc.MemReservation, resMem(res, false), svc.MemLimit,
	} {
		if v > 0 {
			return uint64(v)
		}
	}
	return 0
}

func resMem(res *types.Resources, reservation bool) types.UnitBytes {
	if res == nil {
		return 0
	}
	r := res.Limits
	if reservation {
		r = res.Reservations
	}
	if r == nil {
		return 0
	}
	return r.MemoryBytes
}

func declaredCPUs(svc types.ServiceConfig) float64 {
	if svc.Deploy != nil {
		if r := svc.Deploy.Resources.Reservations; r != nil && r.NanoCPUs > 0 {
			return float64(r.NanoCPUs)
		}
		if r := svc.Deploy.Resources.Limits; r != nil && r.NanoCPUs > 0 {
			return float64(r.NanoCPUs)
		}
	}
	if c := float64(svc.CPUS); c > 0 {
		return c
	}
	return 0
}

// measuredUsage sums the newest metrics samples of the stack's containers
// on the source, per compose service.
func (s *Service) measuredUsage(ctx context.Context, hostID, stack string, list []container.Summary, cores int) map[string]serviceUsage {
	out := map[string]serviceUsage{}
	byName := map[string]metrics.Latest{}
	if s.usage != nil {
		latest, err := s.usage.LatestPerContainer(ctx, hostID, usageMaxAge, cores)
		if err != nil {
			slog.Warn("migration preflight container usage", "host", hostID, "err", err)
		}
		for _, l := range latest {
			byName[l.Name] = l
		}
	}
	for _, c := range list {
		if c.Labels["com.docker.compose.project"] != stack || c.State != "running" {
			continue
		}
		svc := c.Labels["com.docker.compose.service"]
		u := out[svc]
		u.containers++
		for _, name := range c.Names {
			if l, ok := byName[strings.TrimPrefix(name, "/")]; ok {
				u.sampled++
				u.mem += l.MemUsed
				u.cpus += l.CPUPercentCore / 100
				break
			}
		}
		out[svc] = u
	}
	return out
}

// capacityChecks compares the stack's needs with what the target has
// free. Everything else running there, Swarm tasks included, is part of
// the target's used memory and CPU.
func capacityChecks(n stackNeeds, tgt system.Metrics) []PreflightCheck {
	const mb, gb = 1024 * 1024, 1024 * 1024 * 1024
	check := func(name, status, detail string) PreflightCheck {
		return PreflightCheck{Name: name, Passed: status != CheckFail, Status: status, Detail: detail}
	}
	var out []PreflightCheck

	memFree := sub(tgt.MemTotal, tgt.MemUsed)
	memNeed := float64(n.MemBytes) * 1.2
	memDetail := fmt.Sprintf("stack needs %.0f MB memory (%s + 20%%), target has %.0f MB free",
		memNeed/mb, needSource(n), float64(memFree)/mb)
	switch {
	case n.MemBytes == 0 && len(n.MemUnknown) > 0:
		out = append(out, check("target_capacity", CheckUnknown,
			fmt.Sprintf("memory need unknown (no limits/reservations and no current usage for %s); target has %.0f MB free",
				strings.Join(n.MemUnknown, ", "), float64(memFree)/mb)))
	case memNeed > float64(memFree):
		out = append(out, check("target_capacity", CheckFail, memDetail))
	default:
		if len(n.MemUnknown) > 0 {
			memDetail += "; not counted: " + strings.Join(n.MemUnknown, ", ")
		}
		out = append(out, check("target_capacity", CheckPass, memDetail))
	}

	cpuFree := float64(tgt.CPUCores) - tgt.CPUUsed
	switch {
	case tgt.CPUCores <= 0:
		out = append(out, check("target_cpu", CheckUnknown, "target CPU count unknown"))
	case n.CPUs == 0 && len(n.CPUUnknown) > 0:
		out = append(out, check("target_cpu", CheckUnknown,
			fmt.Sprintf("CPU need unknown for %s; target has %.1f cores free", strings.Join(n.CPUUnknown, ", "), cpuFree)))
	case n.CPUs > cpuFree:
		out = append(out, check("target_cpu", CheckFail,
			fmt.Sprintf("stack needs %.2f cores (%s), target has %.1f free", n.CPUs, needSource(n), cpuFree)))
	default:
		out = append(out, check("target_cpu", CheckPass,
			fmt.Sprintf("stack needs %.2f cores (%s), target has %.1f free", n.CPUs, needSource(n), cpuFree)))
	}

	diskFree := sub(tgt.DiskTotal, tgt.DiskUsed)
	diskNeed := float64(n.DiskBytes) * 1.1
	unknown := ""
	if len(n.DiskUnknown) > 0 {
		unknown = "; size unknown: " + strings.Join(n.DiskUnknown, ", ")
	}
	switch {
	case n.DiskBytes == 0 && len(n.DiskUnknown) == 0:
		out = append(out, check("target_disk", CheckPass, "all images present on target"))
	case diskNeed > float64(diskFree):
		out = append(out, check("target_disk", CheckFail,
			fmt.Sprintf("images to pull need %.1f GB (+ 10%%), target has %.1f GB free%s", diskNeed/gb, float64(diskFree)/gb, unknown)))
	case n.DiskBytes == 0:
		out = append(out, check("target_disk", CheckUnknown,
			fmt.Sprintf("target has %.1f GB free%s", float64(diskFree)/gb, unknown)))
	default:
		out = append(out, check("target_disk", CheckPass,
			fmt.Sprintf("images to pull need %.1f GB, target has %.1f GB free%s", diskNeed/gb, float64(diskFree)/gb, unknown)))
	}
	return out
}

func needSource(n stackNeeds) string {
	if n.Measured {
		return "declared resources and current usage"
	}
	return "declared resources"
}

func sub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}

// addSat and mulSat saturate instead of wrapping, so absurd declarations
// in a compose file make the need huge rather than tiny.
func addSat(a, b uint64) uint64 {
	sum, carry := bits.Add64(a, b, 0)
	if carry != 0 {
		return math.MaxUint64
	}
	return sum
}

func mulSat(a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	if hi != 0 {
		return math.MaxUint64
	}
	return lo
}
