package agents

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Limits of one FrameAgentMetrics batch. An agent flushes every minute,
// so a batch normally holds two rounds; after a reconnect it carries the
// rounds buffered while it was away.
const (
	maxMetricsBytes      = 4 << 20
	MaxMetricsHostRows   = 120
	MaxMetricsContainers = 5000
	maxContainerName     = 255
	// Accepted sample times: agents buffer at most 30 minutes, and their
	// clocks may drift a little.
	metricsMaxAge  = 2 * time.Hour
	metricsMaxSkew = 5 * time.Minute
)

// MetricsHostSample is one host sample recorded by an agent.
type MetricsHostSample struct {
	TS         int64   `json:"ts"`
	CPUPercent float64 `json:"cpu_percent"`
	CPUCores   int     `json:"cpu_cores"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	DiskUsed   uint64  `json:"disk_used"`
	DiskTotal  uint64  `json:"disk_total"`
}

// MetricsContainerSample is one container sample recorded by an agent;
// CPUPercentCore is 100 per busy core.
type MetricsContainerSample struct {
	TS             int64   `json:"ts"`
	Name           string  `json:"name"`
	CPUPercentCore float64 `json:"cpu_percent_core"`
	OnlineCPUs     uint32  `json:"online_cpus"`
	MemUsed        uint64  `json:"mem_used"`
	MemLimit       uint64  `json:"mem_limit"`
	NetRx          uint64  `json:"net_rx"`
	NetTx          uint64  `json:"net_tx"`
	BlkRead        uint64  `json:"blk_read"`
	BlkWrite       uint64  `json:"blk_write"`
}

// MetricsBatch is the payload of FrameAgentMetrics.
type MetricsBatch struct {
	Host       []MetricsHostSample      `json:"host,omitempty"`
	Containers []MetricsContainerSample `json:"containers,omitempty"`
}

// MetricsSink stores a batch under the id of the agent that sent it.
type MetricsSink func(ctx context.Context, agentID string, b MetricsBatch) error

// SetMetricsSink makes the server accept agent history; without a sink
// it doesn't offer CapMetricsHistory and ignores the frames.
func (s *Service) SetMetricsSink(fn MetricsSink) { s.metricsSink = fn }

// Sanitize drops samples with a timestamp outside [now-2h, now+5min],
// container samples without a name or with an overlong one, and anything
// beyond the per-batch limits.
func (b *MetricsBatch) Sanitize(now time.Time) {
	lo, hi := now.Add(-metricsMaxAge).Unix(), now.Add(metricsMaxSkew).Unix()
	host := b.Host[:0]
	for _, h := range b.Host {
		if h.TS >= lo && h.TS <= hi && len(host) < MaxMetricsHostRows {
			host = append(host, h)
		}
	}
	b.Host = host
	ctrs := b.Containers[:0]
	for _, c := range b.Containers {
		if c.TS >= lo && c.TS <= hi && c.Name != "" && len(c.Name) <= maxContainerName &&
			len(ctrs) < MaxMetricsContainers {
			ctrs = append(ctrs, c)
		}
	}
	b.Containers = ctrs
}

// handleMetrics stores one batch. The host id is the agent's own, from
// its client certificate; nothing in the payload names a host.
func (s *Service) handleMetrics(ctx context.Context, ag *ConnectedAgent, payload json.RawMessage) {
	if s.metricsSink == nil {
		return
	}
	var b MetricsBatch
	if err := json.Unmarshal(payload, &b); err != nil {
		slog.Debug("agent metrics decode", "agent", ag.Name, "err", err)
		return
	}
	b.Sanitize(time.Now())
	if len(b.Host) == 0 && len(b.Containers) == 0 {
		return
	}
	if err := s.metricsSink(ctx, ag.ID, b); err != nil {
		slog.Warn("agent metrics store", "agent", ag.Name, "err", err)
	}
}

// RecordsHistory reports whether the connected agent ships history.
func (a *ConnectedAgent) RecordsHistory() bool { return a.Hello.Has(CapMetricsHistory) }
