// Package metrics collects Docker stats samples and downsamples them
// into raw/1m/1h retention tables (concept §15.4). Provides a simple
// query API for the UI to draw historical charts.
package metrics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	dtypes "github.com/docker/docker/api/types"
)

// Retention controls how long each resolution is kept. Rows older than
// the retention are pruned after the downsampler runs.
type Retention struct {
	Raw    time.Duration
	OneMin time.Duration
	OneHr  time.Duration
}

var DefaultRetention = Retention{
	Raw:    24 * time.Hour,
	OneMin: 30 * 24 * time.Hour,
	OneHr:  365 * 24 * time.Hour,
}

type Collector struct {
	db        *sql.DB
	docker    *docker.Client
	interval  time.Duration
	retention Retention

	stop chan struct{}
	wg   sync.WaitGroup
}

func NewCollector(db *sql.DB, dockerCli *docker.Client, interval time.Duration, retention Retention) *Collector {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if retention.Raw == 0 {
		retention = DefaultRetention
	}
	return &Collector{
		db:        db,
		docker:    dockerCli,
		interval:  interval,
		retention: retention,
		stop:      make(chan struct{}),
	}
}

// Start kicks off the collection + downsampling goroutines. Call Stop to
// signal shutdown and wait for them to drain.
func (c *Collector) Start(ctx context.Context) {
	if c.docker == nil {
		slog.Warn("metrics: docker unavailable, collector disabled")
		return
	}
	c.wg.Add(2)
	go c.collectLoop(ctx)
	go c.downsampleLoop(ctx)
	slog.Info("metrics collector started", "interval", c.interval)
}

func (c *Collector) Stop() {
	close(c.stop)
	c.wg.Wait()
}

// collectLoop polls Docker stats every interval. It runs one collect
// immediately on start so the first charts appear quickly.
func (c *Collector) collectLoop(ctx context.Context) {
	defer c.wg.Done()
	// Immediate first sample so we don't wait 30s for the chart to populate.
	c.collect(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

func (c *Collector) collect(ctx context.Context) {
	containers, err := c.docker.ListContainers(ctx, false)
	if err != nil {
		slog.Debug("metrics list containers", "err", err)
		return
	}
	if len(containers) == 0 {
		return
	}
	cli := c.docker.Raw()
	ts := time.Now().Unix()

	// Bounded parallelism — 8 concurrent stats calls is plenty and avoids
	// hammering the daemon.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, ct := range containers {
		ct := ct
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			name := sanitizeName(ct.Names)
			if name == "" {
				return
			}
			sample, err := fetchOneShot(ctx, cli, ct.ID)
			if err != nil {
				slog.Debug("metrics stats", "container", name, "err", err)
				return
			}
			c.insertRaw(ctx, name, ts, sample)
		}()
	}
	wg.Wait()
}

// sample is the subset we persist. Mirrors docker.NormalizedStats.
type sample struct {
	CPUPercent float64
	MemUsed    uint64
	MemLimit   uint64
	NetRx      uint64
	NetTx      uint64
	BlkRead    uint64
	BlkWrite   uint64
}

// fetchOneShot calls ContainerStatsOneShot which returns a single reading
// without the streaming overhead. CPU % is computed the same way as the
// live ws endpoint so both match.
func fetchOneShot(ctx context.Context, cli interface {
	ContainerStatsOneShot(context.Context, string) (dtypes.ContainerStats, error)
}, id string) (*sample, error) {
	resp, err := cli.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var s dtypes.StatsJSON
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}

	// CPU % (Linux formula — Windows skipped per concept §10).
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

	memUsed := s.MemoryStats.Usage
	if cache, ok := s.MemoryStats.Stats["cache"]; ok && cache < memUsed {
		memUsed -= cache
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

	return &sample{
		CPUPercent: cpuPct,
		MemUsed:    memUsed,
		MemLimit:   s.MemoryStats.Limit,
		NetRx:      rx,
		NetTx:      tx,
		BlkRead:    blkR,
		BlkWrite:   blkW,
	}, nil
}

func (c *Collector) insertRaw(ctx context.Context, name string, ts int64, s *sample) {
	_, err := c.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO metrics_raw
			(container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		name, ts, s.CPUPercent, s.MemUsed, s.MemLimit, s.NetRx, s.NetTx, s.BlkRead, s.BlkWrite)
	if err != nil {
		slog.Debug("metrics insert", "err", err)
	}
}

// -----------------------------------------------------------------------------
// Downsampling
// -----------------------------------------------------------------------------

func (c *Collector) downsampleLoop(ctx context.Context) {
	defer c.wg.Done()
	// Run once on start to catch up if the server was down.
	c.downsample(ctx)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case <-ticker.C:
			c.downsample(ctx)
		}
	}
}

// downsample rolls older raw samples into the 1m table, older 1m samples
// into the 1h table, and prunes anything past each retention window.
//
// Aggregation uses AVG for CPU/memory and MAX for network/blkio counters
// since those are monotonic — MAX of each bucket preserves "latest value
// seen in the bucket" which is what the UI wants to subtract for rates.
func (c *Collector) downsample(ctx context.Context) {
	now := time.Now().Unix()

	// raw → 1m (samples older than 1h)
	if _, err := c.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO metrics_1m
			(container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
		SELECT
			container_name,
			(ts / 60) * 60 AS bucket,
			AVG(cpu_percent), AVG(mem_used), MAX(mem_limit),
			MAX(net_rx), MAX(net_tx), MAX(blk_read), MAX(blk_write)
		FROM metrics_raw
		WHERE ts < ?
		GROUP BY container_name, bucket`, now-3600); err != nil {
		slog.Warn("metrics downsample raw→1m", "err", err)
	}

	// 1m → 1h (samples older than 7 days)
	if _, err := c.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO metrics_1h
			(container_name, ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
		SELECT
			container_name,
			(ts / 3600) * 3600 AS bucket,
			AVG(cpu_percent), AVG(mem_used), MAX(mem_limit),
			MAX(net_rx), MAX(net_tx), MAX(blk_read), MAX(blk_write)
		FROM metrics_1m
		WHERE ts < ?
		GROUP BY container_name, bucket`, now-7*86400); err != nil {
		slog.Warn("metrics downsample 1m→1h", "err", err)
	}

	// Prune outside retention windows.
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM metrics_raw WHERE ts < ?`, now-int64(c.retention.Raw.Seconds())); err != nil {
		slog.Warn("metrics prune raw", "err", err)
	}
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM metrics_1m WHERE ts < ?`, now-int64(c.retention.OneMin.Seconds())); err != nil {
		slog.Warn("metrics prune 1m", "err", err)
	}
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM metrics_1h WHERE ts < ?`, now-int64(c.retention.OneHr.Seconds())); err != nil {
		slog.Warn("metrics prune 1h", "err", err)
	}
}

func sanitizeName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// Query is used by the handler to fetch historical samples.
type Query struct {
	Name       string
	From       time.Time
	To         time.Time
	Resolution string // "raw" | "1m" | "1h"
}

type Sample struct {
	TS         int64   `json:"ts"`
	CPUPercent float64 `json:"cpu_percent"`
	MemUsed    uint64  `json:"mem_used"`
	MemLimit   uint64  `json:"mem_limit"`
	NetRx      uint64  `json:"net_rx"`
	NetTx      uint64  `json:"net_tx"`
	BlkRead    uint64  `json:"blk_read"`
	BlkWrite   uint64  `json:"blk_write"`
}

func (c *Collector) Query(ctx context.Context, q Query) ([]Sample, error) {
	table := "metrics_raw"
	switch q.Resolution {
	case "1m":
		table = "metrics_1m"
	case "1h":
		table = "metrics_1h"
	case "", "raw":
		// default
	default:
		return nil, errors.New("resolution must be raw|1m|1h")
	}
	if q.From.IsZero() {
		q.From = time.Now().Add(-1 * time.Hour)
	}
	if q.To.IsZero() {
		q.To = time.Now()
	}

	rows, err := c.db.QueryContext(ctx, `
		SELECT ts, cpu_percent, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write
		FROM `+table+`
		WHERE container_name = ? AND ts BETWEEN ? AND ?
		ORDER BY ts ASC`,
		q.Name, q.From.Unix(), q.To.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Sample
	for rows.Next() {
		var s Sample
		if err := rows.Scan(&s.TS, &s.CPUPercent, &s.MemUsed, &s.MemLimit,
			&s.NetRx, &s.NetTx, &s.BlkRead, &s.BlkWrite); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
