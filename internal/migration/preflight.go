package migration

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/compose"
)

// runPreflight executes all pre-flight checks and returns the result.
// If any check fails, the migration should not proceed.
func (s *Service) runPreflight(ctx context.Context, m *Migration) (*PreflightResult, error) {
	result := &PreflightResult{Passed: true}
	addStatus := func(name, status, detail string) {
		result.Checks = append(result.Checks, PreflightCheck{
			Name: name, Passed: status != CheckFail, Status: status, Detail: detail,
		})
		if status == CheckFail {
			result.Passed = false
		}
	}
	add := func(name string, passed bool, detail string) {
		status := CheckPass
		if !passed {
			status = CheckFail
		}
		addStatus(name, status, detail)
	}

	// 1. Target agent online
	target, err := s.hosts.Pick(m.TargetHostID)
	if err != nil {
		add("target_online", false, fmt.Sprintf("target host unavailable: %v", err))
		return result, nil
	}
	add("target_online", true, target.Name())

	if vols, err := s.namedVolumes(ctx, m.StackName); err != nil {
		add("named_volumes", false, err.Error())
	} else if len(vols) > 0 {
		add("named_volumes", false, fmt.Sprintf("%s: %s", ErrNamedVolumes.Error(), strings.Join(vols, ", ")))
	} else {
		add("named_volumes", true, "no named volumes")
	}

	// 2. No other active migration for this stack (already checked at
	//    initiation, but re-verify since we may have been queued).
	hasActive, _ := s.store.HasActive(ctx, m.StackName)
	// Discount our own migration.
	activeMigs, _ := s.store.ListActive(ctx)
	otherActive := 0
	for _, am := range activeMigs {
		if am.StackName == m.StackName && am.ID != m.ID {
			otherActive++
		}
	}
	if otherActive > 0 && hasActive {
		add("no_concurrent", false, "another migration is already active for this stack")
	} else {
		add("no_concurrent", true, "")
	}

	// 3. Source host metrics.
	source, err := s.hosts.Pick(m.SourceHostID)
	if err != nil {
		add("source_online", false, fmt.Sprintf("source host unavailable: %v", err))
		return result, nil
	}
	add("source_online", true, source.Name())

	srcMetrics, err := source.SystemMetrics(ctx)
	if err != nil {
		add("source_metrics", false, fmt.Sprintf("cannot read source metrics: %v", err))
	} else {
		add("source_metrics", true, fmt.Sprintf("CPU: %.0f%%, Mem: %.0f%%", srcMetrics.CPUPercent, srcMetrics.MemPercent))
	}

	// 4. Target capacity check (20% headroom).
	tgtMetrics, err := target.SystemMetrics(ctx)
	if err != nil {
		add("target_capacity", false, fmt.Sprintf("cannot read target metrics: %v", err))
	} else {
		memFree := tgtMetrics.MemTotal - tgtMetrics.MemUsed
		memNeeded := srcMetrics.MemUsed
		if memNeeded > 0 {
			headroom := float64(memFree) / float64(memNeeded)
			if headroom < 1.2 {
				add("target_capacity", false,
					fmt.Sprintf("target has %.0f MB free, needs %.0f MB (source used + 20%% headroom)",
						float64(memFree)/(1024*1024), float64(memNeeded)*1.2/(1024*1024)))
			} else {
				add("target_capacity", true,
					fmt.Sprintf("%.0f MB free (%.0fx source usage)", float64(memFree)/(1024*1024), headroom))
			}
		} else {
			add("target_capacity", true, "source memory usage unknown — skipping check")
		}

		// Disk check: need source disk usage * 1.1 free on target.
		diskFree := tgtMetrics.DiskTotal - tgtMetrics.DiskUsed
		diskNeeded := srcMetrics.DiskUsed
		if diskNeeded > 0 {
			if float64(diskFree) < float64(diskNeeded)*1.1 {
				add("target_disk", false,
					fmt.Sprintf("target has %.0f GB free, needs %.0f GB (source + 10%%)",
						float64(diskFree)/(1024*1024*1024), float64(diskNeeded)*1.1/(1024*1024*1024)))
			} else {
				add("target_disk", true,
					fmt.Sprintf("%.0f GB free", float64(diskFree)/(1024*1024*1024)))
			}
		} else {
			add("target_disk", true, "source disk usage unknown — skipping check")
		}
	}

	// 5. Platform match.
	srcInfo, srcErr := s.hosts.DaemonInfo(ctx, m.SourceHostID)
	tgtInfo, tgtErr := s.hosts.DaemonInfo(ctx, m.TargetHostID)
	if srcErr != nil || tgtErr != nil {
		slog.Warn("migration preflight daemon info", "id", m.ID, "source_err", srcErr, "target_err", tgtErr)
	}
	status, archDetail := archCheck(srcInfo, tgtInfo)
	addStatus("arch_match", status, archDetail)

	// 6. Images available on target.
	detail, err := s.stacks.Get(m.StackName)
	if err != nil {
		add("images", false, fmt.Sprintf("cannot read stack: %v", err))
	} else {
		dir, _ := s.stacks.Dir(m.StackName)
		proj, err := compose.LoadProject(ctx, dir, m.StackName, detail.Env)
		if err != nil {
			add("images", false, fmt.Sprintf("cannot parse compose: %v", err))
		} else {
			var missing []string
			tgtImages, _ := target.ListImages(ctx, false)
			tgtImageSet := make(map[string]bool)
			for _, img := range tgtImages {
				for _, tag := range img.RepoTags {
					tgtImageSet[tag] = true
				}
			}
			for _, svc := range proj.Services {
				if svc.Image != "" && !tgtImageSet[svc.Image] {
					missing = append(missing, svc.Image)
				}
			}
			if len(missing) > 0 {
				// Not a hard failure — images will be pulled in prepare phase.
				add("images", true,
					fmt.Sprintf("%d image(s) to pull on target: %v", len(missing), missing))
			} else {
				add("images", true, "all images present on target")
			}
		}
	}

	slog.Info("migration preflight",
		"id", m.ID, "stack", m.StackName,
		"passed", result.Passed, "checks", len(result.Checks))
	return result, nil
}

// archCheck compares the platforms the two daemons report. A host whose
// agent doesn't report one makes the result unknown: guessing "pass"
// would move an amd64-only stack onto an arm64 host unchecked.
func archCheck(src, tgt *agents.DaemonInfo) (status, detail string) {
	platform := func(d *agents.DaemonInfo) string {
		if d == nil || d.Arch == "" {
			return ""
		}
		if d.OS == "" {
			return d.Arch
		}
		return d.OS + "/" + d.Arch
	}
	sp, tp := platform(src), platform(tgt)
	switch {
	case sp == "" && tp == "":
		return CheckUnknown, "neither host reports its architecture; update their agents"
	case sp == "":
		return CheckUnknown, "source host doesn't report its architecture; update its agent (target " + tp + ")"
	case tp == "":
		return CheckUnknown, "target host doesn't report its architecture; update its agent (source " + sp + ")"
	case sp != tp:
		return CheckFail, fmt.Sprintf("source=%s, target=%s", sp, tp)
	}
	return CheckPass, tp
}
