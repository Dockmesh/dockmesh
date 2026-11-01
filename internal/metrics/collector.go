// Package metrics collects container and host samples and downsamples
// them into raw/1m/1h retention tables. Provides a simple query API for
// the UI to draw historical charts.
package metrics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/system"
)

// LocalHostID is the host id of the server's own Docker daemon.
const LocalHostID = "local"

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

// HostSampler returns the current metrics of the server host.
type HostSampler func() system.Metrics

type Collector struct {
	db         *sql.DB
	docker     *docker.Client
	hostSample HostSampler
	interval   time.Duration
	retention  Retention

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
		db:         db,
		docker:     dockerCli,
		hostSample: system.Collect,
		interval:   interval,
		retention:  retention,
		stop:       make(chan struct{}),
	}
}

// Start kicks off the collection + downsampling goroutines. Call Stop to
// signal shutdown and wait for them to drain.
func (c *Collector) Start(ctx context.Context) {
	if c.docker == nil {
		slog.Warn("metrics: docker unavailable, collecting host metrics only")
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

// collectLoop samples every interval, starting right away so the first
// charts appear quickly.
func (c *Collector) collectLoop(ctx context.Context) {
	defer c.wg.Done()
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
	ts := time.Now().Unix()
	if c.hostSample != nil {
		if err := c.InsertHost(ctx, LocalHostID, ts, c.hostSample()); err != nil {
			slog.Debug("metrics insert host", "err", err)
		}
	}
	if c.docker == nil {
		return
	}
	containers, err := c.docker.ListContainers(ctx, false)
	if err != nil {
		slog.Debug("metrics list containers", "err", err)
		return
	}
	cli := c.docker.Raw()

	// Bounded parallelism — 8 concurrent stats calls is plenty and avoids
	// hammering the daemon.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, ct := range containers {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			name := sanitizeName(ct.Names)
			if name == "" {
				return
			}
			s, err := docker.SampleStats(ctx, cli, ct.ID)
			if err != nil {
				slog.Debug("metrics stats", "container", name, "err", err)
				return
			}
			if err := c.InsertContainer(ctx, LocalHostID, name, ts, s); err != nil {
				slog.Debug("metrics insert", "err", err)
			}
		}()
	}
	wg.Wait()
}

// InsertContainer stores one container sample of hostID.
func (c *Collector) InsertContainer(ctx context.Context, hostID, name string, ts int64, s docker.NormalizedStats) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO metrics_raw
			(host_id, container_name, ts, cpu_percent, cpu_cores, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		hostID, name, ts, s.CPUPercentCore, s.OnlineCPUs, s.MemUsed, s.MemLimit, s.NetRx, s.NetTx, s.BlkRead, s.BlkWrite)
	return err
}

// InsertHost stores one host sample of hostID.
func (c *Collector) InsertHost(ctx context.Context, hostID string, ts int64, m system.Metrics) error {
	_, err := c.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO host_metrics_raw
			(host_id, ts, cpu_percent, cpu_cores, mem_used, mem_total, disk_used, disk_total)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		hostID, ts, m.CPUPercent, m.CPUCores, m.MemUsed, m.MemTotal, m.DiskUsed, m.DiskTotal)
	return err
}

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

// series describes one raw/1m/1h table family for downsampling.
type series struct {
	prefix string // table name without the _raw/_1m/_1h suffix
	keys   string // key columns besides ts
	cols   string // value columns, in the order of aggs
	aggs   string // aggregate of each value column per bucket
}

// Network and block I/O are monotonic counters: MAX keeps the latest
// value of a bucket, which is what a reader subtracts for rates.
var seriesTables = []series{
	{
		prefix: "metrics",
		keys:   "host_id, container_name",
		cols:   "cpu_percent, cpu_cores, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write",
		aggs:   "AVG(cpu_percent), MAX(cpu_cores), AVG(mem_used), MAX(mem_limit), MAX(net_rx), MAX(net_tx), MAX(blk_read), MAX(blk_write)",
	},
	{
		prefix: "host_metrics",
		keys:   "host_id",
		cols:   "cpu_percent, cpu_cores, mem_used, mem_total, disk_used, disk_total",
		aggs:   "AVG(cpu_percent), MAX(cpu_cores), AVG(mem_used), MAX(mem_total), AVG(disk_used), MAX(disk_total)",
	},
}

// downsample rolls raw samples older than 1h into the 1m table, 1m
// samples older than 7 days into the 1h table, and prunes anything past
// each retention window.
func (c *Collector) downsample(ctx context.Context) {
	now := time.Now().Unix()
	keep := []struct {
		suffix string
		age    time.Duration
	}{{"_raw", c.retention.Raw}, {"_1m", c.retention.OneMin}, {"_1h", c.retention.OneHr}}
	for _, t := range seriesTables {
		c.rollup(ctx, t, "_raw", "_1m", 60, now-3600)
		c.rollup(ctx, t, "_1m", "_1h", 3600, now-7*86400)
		for _, k := range keep {
			if _, err := c.db.ExecContext(ctx,
				`DELETE FROM `+t.prefix+k.suffix+` WHERE ts < ?`, now-int64(k.age.Seconds())); err != nil {
				slog.Warn("metrics prune", "table", t.prefix+k.suffix, "err", err)
			}
		}
	}
}

func (c *Collector) rollup(ctx context.Context, t series, from, to string, bucket, before int64) {
	q := fmt.Sprintf(`
		INSERT OR REPLACE INTO %[2]s (%[3]s, ts, %[4]s)
		SELECT %[3]s, (ts / %[6]d) * %[6]d AS bucket, %[5]s
		  FROM %[1]s
		 WHERE ts < ?
		 GROUP BY %[3]s, bucket`, t.prefix+from, t.prefix+to, t.keys, t.cols, t.aggs, bucket)
	if _, err := c.db.ExecContext(ctx, q, before); err != nil {
		slog.Warn("metrics downsample", "from", t.prefix+from, "to", t.prefix+to, "err", err)
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
	HostID     string // defaults to LocalHostID
	Name       string // container name; unused for host queries
	From       time.Time
	To         time.Time
	Resolution string // "raw" | "1m" | "1h"
	// Cores is used for rows stored without a core count.
	Cores int
}

// Sample is one stored container sample. CPUPercent is 0..100 of the
// host, CPUPercentCore 100 per busy core as `docker stats` shows it.
type Sample struct {
	TS             int64   `json:"ts"`
	CPUPercent     float64 `json:"cpu_percent"`
	CPUPercentCore float64 `json:"cpu_percent_core"`
	MemUsed        uint64  `json:"mem_used"`
	MemLimit       uint64  `json:"mem_limit"`
	NetRx          uint64  `json:"net_rx"`
	NetTx          uint64  `json:"net_tx"`
	BlkRead        uint64  `json:"blk_read"`
	BlkWrite       uint64  `json:"blk_write"`
}

// HostSample is one stored host sample; CPUPercent is 0..100.
type HostSample struct {
	TS         int64   `json:"ts"`
	CPUPercent float64 `json:"cpu_percent"`
	CPUCores   int     `json:"cpu_cores"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	DiskUsed   uint64  `json:"disk_used"`
	DiskTotal  uint64  `json:"disk_total"`
}

// Latest is the newest sample of one container.
type Latest struct {
	Name           string  `json:"name"`
	TS             int64   `json:"ts"`
	CPUPercent     float64 `json:"cpu_percent"`
	CPUPercentCore float64 `json:"cpu_percent_core"`
	MemUsed        uint64  `json:"mem_used"`
	MemLimit       uint64  `json:"mem_limit"`
}

func hostOrLocal(id string) string {
	if id == "" {
		return LocalHostID
	}
	return id
}

// hostPercent converts a per-core percentage to 0..100 of the host.
func hostPercent(perCore float64, cores, fallback int) float64 {
	if cores <= 0 {
		cores = fallback
	}
	if cores <= 0 {
		return perCore
	}
	return perCore / float64(cores)
}

// LatestPerContainer returns the newest sample of every container of
// hostID that reported within the last maxAgeSec seconds (default 60).
// cores is used for rows stored without a core count.
func (c *Collector) LatestPerContainer(ctx context.Context, hostID string, maxAgeSec int64, cores int) ([]Latest, error) {
	if maxAgeSec <= 0 {
		maxAgeSec = 60
	}
	hostID = hostOrLocal(hostID)
	cutoff := time.Now().Unix() - maxAgeSec
	rows, err := c.db.QueryContext(ctx, `
		SELECT m.container_name, m.ts, m.cpu_percent, m.cpu_cores, m.mem_used, m.mem_limit
		  FROM metrics_raw m
		  JOIN (
		      SELECT container_name, MAX(ts) AS maxts
		        FROM metrics_raw
		       WHERE host_id = ? AND ts >= ?
		    GROUP BY container_name
		  ) latest
		    ON latest.container_name = m.container_name
		   AND latest.maxts = m.ts
		 WHERE m.host_id = ?`, hostID, cutoff, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Latest{}
	for rows.Next() {
		var l Latest
		var rowCores int
		if err := rows.Scan(&l.Name, &l.TS, &l.CPUPercentCore, &rowCores, &l.MemUsed, &l.MemLimit); err != nil {
			return nil, err
		}
		l.CPUPercent = hostPercent(l.CPUPercentCore, rowCores, cores)
		out = append(out, l)
	}
	return out, rows.Err()
}

// HasHistory reports whether any host or container sample of hostID is
// stored.
func (c *Collector) HasHistory(ctx context.Context, hostID string) (bool, error) {
	var found bool
	err := c.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM host_metrics_raw WHERE host_id = ?)
		    OR EXISTS (SELECT 1 FROM host_metrics_1m WHERE host_id = ?)
		    OR EXISTS (SELECT 1 FROM host_metrics_1h WHERE host_id = ?)
		    OR EXISTS (SELECT 1 FROM metrics_raw WHERE host_id = ?)
		    OR EXISTS (SELECT 1 FROM metrics_1m WHERE host_id = ?)
		    OR EXISTS (SELECT 1 FROM metrics_1h WHERE host_id = ?)`,
		hostID, hostID, hostID, hostID, hostID, hostID).Scan(&found)
	return found, err
}

// ErrResolution is returned for a resolution other than raw, 1m or 1h.
var ErrResolution = errors.New("resolution must be raw|1m|1h")

func resolutionSuffix(res string) (string, error) {
	switch res {
	case "", "raw":
		return "_raw", nil
	case "1m":
		return "_1m", nil
	case "1h":
		return "_1h", nil
	}
	return "", ErrResolution
}

// maxSpan bounds a query to roughly the retention of its table, so one
// request reads at most a few thousand rows per series.
var maxSpan = map[string]time.Duration{
	"_raw": 25 * time.Hour,
	"_1m":  31 * 24 * time.Hour,
	"_1h":  366 * 24 * time.Hour,
}

func (q *Query) window(suffix string) {
	if q.To.IsZero() {
		q.To = time.Now()
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-1 * time.Hour)
	}
	if limit := q.To.Add(-maxSpan[suffix]); q.From.Before(limit) {
		q.From = limit
	}
}

// Query returns the samples of one container.
func (c *Collector) Query(ctx context.Context, q Query) ([]Sample, error) {
	suffix, err := resolutionSuffix(q.Resolution)
	if err != nil {
		return nil, err
	}
	q.window(suffix)
	rows, err := c.db.QueryContext(ctx, `
		SELECT ts, cpu_percent, cpu_cores, mem_used, mem_limit, net_rx, net_tx, blk_read, blk_write
		FROM metrics`+suffix+`
		WHERE host_id = ? AND container_name = ? AND ts BETWEEN ? AND ?
		ORDER BY ts ASC`,
		hostOrLocal(q.HostID), q.Name, q.From.Unix(), q.To.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Sample{}
	for rows.Next() {
		var s Sample
		var cores int
		if err := rows.Scan(&s.TS, &s.CPUPercentCore, &cores, &s.MemUsed, &s.MemLimit,
			&s.NetRx, &s.NetTx, &s.BlkRead, &s.BlkWrite); err != nil {
			return nil, err
		}
		s.CPUPercent = hostPercent(s.CPUPercentCore, cores, q.Cores)
		out = append(out, s)
	}
	return out, rows.Err()
}

// QueryHost returns the host samples of q.HostID.
func (c *Collector) QueryHost(ctx context.Context, q Query) ([]HostSample, error) {
	suffix, err := resolutionSuffix(q.Resolution)
	if err != nil {
		return nil, err
	}
	q.window(suffix)
	rows, err := c.db.QueryContext(ctx, `
		SELECT ts, cpu_percent, cpu_cores, mem_used, mem_total, disk_used, disk_total
		FROM host_metrics`+suffix+`
		WHERE host_id = ? AND ts BETWEEN ? AND ?
		ORDER BY ts ASC`,
		hostOrLocal(q.HostID), q.From.Unix(), q.To.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []HostSample{}
	for rows.Next() {
		var s HostSample
		if err := rows.Scan(&s.TS, &s.CPUPercent, &s.CPUCores, &s.MemUsed, &s.MemTotal,
			&s.DiskUsed, &s.DiskTotal); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
