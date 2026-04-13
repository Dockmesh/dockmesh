package docker

import (
	"context"
	"encoding/json"
	"io"

	"github.com/docker/docker/api/types"
)

// NormalizedStats is the minimal stats payload the UI needs. Computed
// server-side so the wire format stays tiny and the frontend doesn't have
// to know Docker's full StatsJSON layout.
type NormalizedStats struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsed    uint64  `json:"mem_used"`
	MemLimit   uint64  `json:"mem_limit"`
	MemPercent float64 `json:"mem_percent"`
	NetRx      uint64  `json:"net_rx"`
	NetTx      uint64  `json:"net_tx"`
	BlkRead    uint64  `json:"blk_read"`
	BlkWrite   uint64  `json:"blk_write"`
	PidsCurr   uint64  `json:"pids_current"`
}

// ContainerStats returns the raw stats stream from the docker daemon —
// newline-delimited JSON matching the StatsJSON layout. Same wire format
// the local handler and the remote agent use, so normalization can stay
// in one place (the WS handler).
func (c *Client) ContainerStats(ctx context.Context, containerID string) (io.ReadCloser, error) {
	resp, err := c.cli.ContainerStats(ctx, containerID, true)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// StreamStats returns a channel of normalized stats and an error channel.
// The channels close when ctx is cancelled or the stats stream ends.
func (c *Client) StreamStats(ctx context.Context, containerID string) (<-chan NormalizedStats, <-chan error) {
	out := make(chan NormalizedStats, 4)
	errCh := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errCh)

		resp, err := c.cli.ContainerStats(ctx, containerID, true)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()

		dec := json.NewDecoder(resp.Body)
		for {
			if ctx.Err() != nil {
				return
			}
			var s types.StatsJSON
			if err := dec.Decode(&s); err != nil {
				if err != io.EOF && ctx.Err() == nil {
					errCh <- err
				}
				return
			}
			select {
			case out <- normalize(&s):
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, errCh
}

// Normalize is the exported version of normalize() so handlers that
// stream raw stats (local or via remote agent) can convert frame-by-frame
// without reimplementing the formulas.
func Normalize(s *types.StatsJSON) NormalizedStats { return normalize(s) }

func normalize(s *types.StatsJSON) NormalizedStats {
	// CPU % — Linux formula (Windows path omitted; concept §10 targets Linux).
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage - s.PreCPUStats.SystemUsage)
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	var cpuPct float64
	if sysDelta > 0 && cpuDelta > 0 {
		cpuPct = (cpuDelta / sysDelta) * cpus * 100
	}

	// Memory — subtract page cache to match what `docker stats` shows.
	memUsed := s.MemoryStats.Usage
	if cache, ok := s.MemoryStats.Stats["cache"]; ok {
		if cache < memUsed {
			memUsed -= cache
		}
	}
	var memPct float64
	if s.MemoryStats.Limit > 0 {
		memPct = float64(memUsed) / float64(s.MemoryStats.Limit) * 100
	}

	// Network — sum across interfaces.
	var rx, tx uint64
	for _, n := range s.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}

	// Block I/O.
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
		CPUPercent: cpuPct,
		MemUsed:    memUsed,
		MemLimit:   s.MemoryStats.Limit,
		MemPercent: memPct,
		NetRx:      rx,
		NetTx:      tx,
		BlkRead:    blkR,
		BlkWrite:   blkW,
		PidsCurr:   s.PidsStats.Current,
	}
}
