package migration

import (
	"context"
	"fmt"
	"sort"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// PlanEntry is one stack → target assignment in a drain plan.
type PlanEntry struct {
	StackName    string `json:"stack_name"`
	TargetHostID string `json:"target_host_id"`
	TargetName   string `json:"target_name"`
	// Estimated weight used for bin-packing (bytes, rough).
	WeightBytes int64  `json:"weight_bytes"`
	// Whether the assignment passed capacity check.
	Feasible     bool   `json:"feasible"`
	Detail       string `json:"detail,omitempty"`
}

// DrainPlan is the result of PlanDrain.
type DrainPlan struct {
	SourceHostID string      `json:"source_host_id"`
	SourceName   string      `json:"source_name"`
	Entries      []PlanEntry `json:"entries"`
	Feasible     bool        `json:"feasible"` // all entries feasible
}

// PlanDrain generates a bin-packing plan to drain all stacks from a host
// to available targets. Greedy: sort stacks descending by weight, place
// each on the target with most free capacity.
func PlanDrain(
	ctx context.Context,
	sourceHostID string,
	hosts *host.Registry,
	deployStore *stacks.DeploymentStore,
	stacksMgr *stacks.Manager,
) (*DrainPlan, error) {
	// Get source host info.
	source, err := hosts.Pick(sourceHostID)
	if err != nil {
		return nil, fmt.Errorf("source host: %w", err)
	}

	// Find all stacks deployed on source.
	deps, err := deployStore.ByHost(ctx, sourceHostID)
	if err != nil {
		return nil, err
	}
	if len(deps) == 0 {
		return &DrainPlan{
			SourceHostID: sourceHostID,
			SourceName:   source.Name(),
			Feasible:     true,
		}, nil
	}

	// Get target hosts (all online hosts except source).
	allHosts, err := hosts.List(ctx)
	if err != nil {
		return nil, err
	}
	type targetCap struct {
		id       string
		name     string
		freeBytes int64
	}
	var targets []targetCap
	for _, h := range allHosts {
		if h.ID == sourceHostID || h.Status != "online" {
			continue
		}
		// Get metrics for free capacity estimation.
		t, pickErr := hosts.Pick(h.ID)
		if pickErr != nil {
			continue
		}
		m, mErr := t.SystemMetrics(ctx)
		if mErr != nil {
			continue
		}
		freeBytes := int64(m.DiskTotal - m.DiskUsed)
		targets = append(targets, targetCap{id: h.ID, name: h.Name, freeBytes: freeBytes})
	}

	if len(targets) == 0 {
		return nil, fmt.Errorf("no online target hosts available")
	}

	// Estimate weight per stack (use source disk as proxy — rough but
	// good enough for greedy placement).
	type stackWeight struct {
		name   string
		weight int64
	}
	var stks []stackWeight
	for _, d := range deps {
		if d.Status == "migrated_away" {
			continue
		}
		// Use a fixed estimate per stack since we can't easily measure
		// per-stack disk usage. 500MB default weight.
		w := int64(500 * 1024 * 1024)
		stks = append(stks, stackWeight{name: d.StackName, weight: w})
	}

	// Sort descending by weight (largest first = better bin-packing).
	sort.Slice(stks, func(i, j int) bool {
		return stks[i].weight > stks[j].weight
	})

	plan := &DrainPlan{
		SourceHostID: sourceHostID,
		SourceName:   source.Name(),
		Feasible:     true,
	}

	for _, s := range stks {
		// Greedy: pick target with most free space.
		bestIdx := -1
		bestFree := int64(-1)
		for i, t := range targets {
			if t.freeBytes > bestFree {
				bestFree = t.freeBytes
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			plan.Entries = append(plan.Entries, PlanEntry{
				StackName: s.name,
				Feasible:  false,
				Detail:    "no target with capacity",
			})
			plan.Feasible = false
			continue
		}

		t := targets[bestIdx]
		feasible := t.freeBytes >= int64(float64(s.weight)*1.1)
		entry := PlanEntry{
			StackName:    s.name,
			TargetHostID: t.id,
			TargetName:   t.name,
			WeightBytes:  s.weight,
			Feasible:     feasible,
		}
		if !feasible {
			entry.Detail = fmt.Sprintf("target %s has %d MB free, needs %d MB",
				t.name, t.freeBytes/(1024*1024), int64(float64(s.weight)*1.1)/(1024*1024))
			plan.Feasible = false
		}
		plan.Entries = append(plan.Entries, entry)

		// Deduct from target capacity for next iteration.
		targets[bestIdx].freeBytes -= s.weight
	}

	return plan, nil
}
