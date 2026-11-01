package main

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/dockmesh/dockmesh/internal/docker"
)

// aggregateDockerStats fans out `ContainerStatsOneShot` across every
// running container and returns the sums expressed as:
//
//   - cpuPercent: total Docker CPU utilisation as a % of the Docker
//     allocation (0..100). Sum of per-container `cpuDelta/systemDelta
//     × num_cpus × 100` normalised by Docker's cpu cap.
//
//   - memUsed: bytes of memory in use across all running containers,
//     without page cache, as `docker stats` shows it.
//
// Concurrency capped at 8 parallel goroutines and a 3-second deadline
// so a hung container or slow daemon can't stall the sampler loop.
// Used by the metrics sampler via SetDockerInfoFn to replace the
// host-wide figures (which are meaningless for Docker Desktop's VM)
// with Docker-specific utilisation.
func aggregateDockerStats(parent context.Context, cli *docker.Client) (cpuPercent float64, memUsed uint64) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()

	list, err := cli.Raw().ContainerList(ctx, container.ListOptions{})
	if err != nil || len(list) == 0 {
		return 0, 0
	}

	var (
		mu         sync.Mutex
		totalCPU   float64 // sum of per-container percent-of-one-core
		totalMem   uint64
		wg         sync.WaitGroup
		maxWorkers = 8
	)
	sem := make(chan struct{}, maxWorkers)

	for _, c := range list {
		if c.State != "running" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()

			resp, err := cli.Raw().ContainerStatsOneShot(ctx, id)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			var stats container.StatsResponse
			if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
				return
			}

			n := docker.Normalize(&stats)

			mu.Lock()
			totalCPU += n.CPUPercentCore
			totalMem += n.MemUsed
			mu.Unlock()
		}(c.ID)
	}
	wg.Wait()

	// Normalise totalCPU (which is "percent of one core" summed across
	// containers, can legitimately exceed 100%) down to 0-100% of the
	// Docker allocation. If a container is saturating 2 cores on a
	// 6-core Docker VM, the tile should read ~33%.
	ncpu := runtime.NumCPU()
	if info, err := cli.Info(ctx); err == nil && info.NCPU > 0 {
		ncpu = info.NCPU
	}
	if ncpu > 0 {
		cpuPercent = totalCPU / float64(ncpu)
	}
	if cpuPercent < 0 {
		cpuPercent = 0
	}
	if cpuPercent > 100 {
		cpuPercent = 100
	}
	memUsed = totalMem
	return
}

// needsDockerLimitCheck returns true on platforms where we should
// assume Docker might be running in a memory-capped VM (macOS,
// Windows). On Linux the daemon runs directly on the host kernel
// so NCPU / MemTotal typically equal the hardware; sampling
// container stats there adds overhead without changing the dashboard.
func needsDockerLimitCheck() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return false
	}
}
