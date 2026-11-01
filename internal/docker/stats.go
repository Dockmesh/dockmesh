package docker

import (
	"context"
	"encoding/json"
	"io"

	"github.com/docker/docker/api/types/container"
)

// NormalizedStats is the stats payload of one container frame, computed
// the same way for the live stream, the history collector and the
// Docker Desktop host tiles.
type NormalizedStats struct {
	// CPUPercent is the share of the whole host, 0..100.
	CPUPercent float64 `json:"cpu_percent"`
	// CPUPercentCore is what `docker stats` shows: 100 per busy core.
	CPUPercentCore float64 `json:"cpu_percent_core"`
	OnlineCPUs     uint32  `json:"online_cpus"`
	MemUsed        uint64  `json:"mem_used"`
	MemLimit       uint64  `json:"mem_limit"`
	MemPercent     float64 `json:"mem_percent"`
	// MemUnlimited is set by callers that know the host RAM, see
	// NoMemLimit.
	MemUnlimited bool   `json:"mem_unlimited,omitempty"`
	NetRx        uint64 `json:"net_rx"`
	NetTx        uint64 `json:"net_tx"`
	BlkRead      uint64 `json:"blk_read"`
	BlkWrite     uint64 `json:"blk_write"`
	PidsCurr     uint64 `json:"pids_current"`
}

// ContainerStats returns the raw stats stream from the docker daemon —
// newline-delimited JSON matching the container.StatsResponse layout. Same wire format
// the local handler and the remote agent use, so normalization can stay
// in one place (Normalize).
func (c *Client) ContainerStats(ctx context.Context, containerID string) (io.ReadCloser, error) {
	resp, err := c.cli.ContainerStats(ctx, containerID, true)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// StatsReader is the part of the Docker SDK client SampleStats needs.
type StatsReader interface {
	ContainerStats(ctx context.Context, id string, stream bool) (container.StatsResponseReader, error)
}

// SampleStats reads two frames of a container's stats stream: the first
// has no PreCPUStats baseline, so its CPU delta is 0.
func SampleStats(ctx context.Context, cli StatsReader, id string) (NormalizedStats, error) {
	resp, err := cli.ContainerStats(ctx, id, true)
	if err != nil {
		return NormalizedStats{}, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	var s container.StatsResponse
	if err := dec.Decode(&s); err != nil {
		return NormalizedStats{}, err
	}
	if err := dec.Decode(&s); err != nil {
		return NormalizedStats{}, err
	}
	return Normalize(&s), nil
}

// Normalize turns one Docker stats frame into NormalizedStats. The CPU
// and memory formulas are the ones of the Docker CLI, so the numbers
// match `docker stats` on cgroup v1 and v2.
func Normalize(s *container.StatsResponse) NormalizedStats {
	cpus := s.CPUStats.OnlineCPUs
	if cpus == 0 {
		cpus = uint32(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	var hostPct float64
	// Counters restart with the container; a frame across the restart
	// would underflow.
	if s.CPUStats.CPUUsage.TotalUsage > s.PreCPUStats.CPUUsage.TotalUsage &&
		s.CPUStats.SystemUsage > s.PreCPUStats.SystemUsage {
		cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage)
		sysDelta := float64(s.CPUStats.SystemUsage - s.PreCPUStats.SystemUsage)
		hostPct = cpuDelta / sysDelta * 100
	}

	memUsed := memUsedNoCache(s.MemoryStats)
	var memPct float64
	if s.MemoryStats.Limit > 0 {
		memPct = float64(memUsed) / float64(s.MemoryStats.Limit) * 100
	}

	var rx, tx uint64
	for _, n := range s.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}

	var blkR, blkW uint64
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch e.Op {
		case "read", "Read":
			blkR += e.Value
		case "write", "Write":
			blkW += e.Value
		}
	}

	return NormalizedStats{
		CPUPercent:     hostPct,
		CPUPercentCore: hostPct * float64(cpus),
		OnlineCPUs:     cpus,
		MemUsed:        memUsed,
		MemLimit:       s.MemoryStats.Limit,
		MemPercent:     memPct,
		NetRx:          rx,
		NetTx:          tx,
		BlkRead:        blkR,
		BlkWrite:       blkW,
		PidsCurr:       s.PidsStats.Current,
	}
}

// memUsedNoCache subtracts the reclaimable page cache: total_inactive_file
// on cgroup v1, inactive_file on cgroup v2 (the key decides the version).
func memUsedNoCache(m container.MemoryStats) uint64 {
	if v, ok := m.Stats["total_inactive_file"]; ok && v < m.Usage {
		return m.Usage - v
	}
	if v := m.Stats["inactive_file"]; v < m.Usage {
		return m.Usage - v
	}
	return m.Usage
}

// NoMemLimit reports whether a container's memory limit is just the host
// RAM, which Docker reports when no limit is set. A limit within 2 % of
// the host RAM is treated the same: it limits nothing in practice.
func NoMemLimit(limit, hostMem uint64) bool {
	if limit == 0 {
		return true
	}
	return hostMem > 0 && limit >= hostMem-hostMem/50
}
