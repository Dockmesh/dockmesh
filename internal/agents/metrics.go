package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"time"
)

// Limits of one FrameAgentMetrics batch. An agent flushes every minute,
// so a batch normally holds two rounds; after a reconnect it carries the
// rounds buffered while it was away.
const (
	maxMetricsBytes      = 4 << 20
	MaxMetricsHostRows   = 120
	MaxMetricsContainers = 5000
	// maxMetricsNames bounds the distinct container names of one batch, so
	// the rows an agent can create are bounded by names × sample slots.
	maxMetricsNames = 1000
	// Accepted sample times after the clock correction: agents buffer at
	// most 30 minutes.
	metricsMaxAge  = 2 * time.Hour
	metricsMaxSkew = 5 * time.Minute
	// metricsSlot is the sampling interval; times are snapped to it.
	metricsSlot = 30
	// MetricsMinGap is the least time between two accepted batches of one
	// agent; an agent catching up waits longer than this between batches.
	MetricsMinGap   = 10 * time.Second
	metricsStoreMax = 30 * time.Second
)

// containerName is Docker's container name charset.
var containerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)

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

// MetricsBatch is the payload of FrameAgentMetrics. SentAt is the agent's
// clock when it sent the batch; the server shifts every sample by the
// difference to its own clock.
type MetricsBatch struct {
	SentAt     int64                    `json:"sent_at"`
	Host       []MetricsHostSample      `json:"host,omitempty"`
	Containers []MetricsContainerSample `json:"containers,omitempty"`
}

// MetricsSink stores a batch under the id of the agent that sent it.
type MetricsSink func(ctx context.Context, agentID string, b MetricsBatch) error

// SetMetricsSink makes the server accept agent history; without a sink
// it doesn't offer CapMetricsHistory and ignores the frames.
func (s *Service) SetMetricsSink(fn MetricsSink) { s.metricsSink = fn }

// Sanitize moves sample times onto the server's clock and the 30 s grid,
// then drops samples outside [now-2h, now+5min], container samples with
// a name Docker wouldn't allow, and anything beyond the per-batch limits.
func (b *MetricsBatch) Sanitize(now time.Time) {
	var shift int64
	if b.SentAt > 0 {
		shift = now.Unix() - b.SentAt
	}
	fix := func(ts int64) int64 {
		ts += shift
		return ts - ts%metricsSlot
	}
	lo, hi := now.Add(-metricsMaxAge).Unix(), now.Add(metricsMaxSkew).Unix()
	host := b.Host[:0]
	for _, h := range b.Host {
		h.TS = fix(h.TS)
		if h.TS >= lo && h.TS <= hi && len(host) < MaxMetricsHostRows {
			host = append(host, h)
		}
	}
	b.Host = host
	names := map[string]struct{}{}
	ctrs := b.Containers[:0]
	for _, c := range b.Containers {
		c.TS = fix(c.TS)
		if c.TS < lo || c.TS > hi || !containerName.MatchString(c.Name) || len(ctrs) >= MaxMetricsContainers {
			continue
		}
		if _, seen := names[c.Name]; !seen {
			if len(names) >= maxMetricsNames {
				continue
			}
			names[c.Name] = struct{}{}
		}
		ctrs = append(ctrs, c)
	}
	b.Containers = ctrs
}

// acceptMetrics reports whether a batch of a may be stored now: one at a
// time, at least MetricsMinGap after the previous one.
func (a *ConnectedAgent) acceptMetrics(now time.Time) bool {
	last := a.lastMetrics.Load()
	if last != 0 && now.UnixNano()-last < int64(MetricsMinGap) {
		return false
	}
	if !a.ingesting.CompareAndSwap(false, true) {
		return false
	}
	a.lastMetrics.Store(now.UnixNano())
	return true
}

// refuseMetrics answers a batch that arrived too early, while another one
// was being stored, or too large; the agent keeps it and retries.
func (a *ConnectedAgent) refuseMetrics(ctx context.Context, f Frame) {
	a.answerMetrics(ctx, f, errors.New("metrics batch refused, retry later"))
}

func (a *ConnectedAgent) answerMetrics(ctx context.Context, f Frame, err error) {
	env := ResponseEnvelope{OK: true, Data: json.RawMessage(`{}`)}
	if err != nil {
		env = ResponseEnvelope{OK: false, Error: err.Error()}
	}
	payload, _ := json.Marshal(env)
	_ = a.enqueue(ctx, Frame{Type: FrameRes, ID: f.ID, Payload: payload})
}

// handleMetrics stores one batch and acknowledges it, so the agent keeps
// what was not stored. The host id is the agent's own, from its client
// certificate; nothing in the payload names a host.
func (s *Service) handleMetrics(ctx context.Context, ag *ConnectedAgent, f Frame) {
	ag.answerMetrics(ctx, f, s.storeMetrics(ctx, ag, f.Payload))
}

func (s *Service) storeMetrics(ctx context.Context, ag *ConnectedAgent, payload json.RawMessage) error {
	if s.metricsSink == nil {
		return errors.New("history not accepted")
	}
	var b MetricsBatch
	if err := json.Unmarshal(payload, &b); err != nil {
		return err
	}
	b.Sanitize(time.Now())
	if len(b.Host) == 0 && len(b.Containers) == 0 {
		return nil
	}
	// A batch already received is stored even if the session ends now.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsStoreMax)
	defer cancel()
	if err := s.metricsSink(ctx, ag.ID, b); err != nil {
		slog.Warn("agent metrics store", "agent", ag.Name, "err", err)
		return errors.New("store failed")
	}
	return nil
}

// RecordsHistory reports whether the connected agent ships history.
func (a *ConnectedAgent) RecordsHistory() bool { return a.Hello.Has(CapMetricsHistory) }

// metricsTables hold history keyed by host_id.
var metricsTables = []string{
	"metrics_raw", "metrics_1m", "metrics_1h",
	"host_metrics_raw", "host_metrics_1m", "host_metrics_1h",
}

// deleteHistory removes the stored history of a deleted agent.
func (s *Service) deleteHistory(ctx context.Context, id string) {
	for _, t := range metricsTables {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM `+t+` WHERE host_id = ?`, id); err != nil {
			slog.Warn("agent delete: metrics history", "table", t, "err", err)
		}
	}
}
