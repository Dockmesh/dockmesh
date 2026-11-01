package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// Limits of one FrameAgentMetrics batch. An agent flushes every minute,
// so a batch normally holds two rounds; after a reconnect it carries the
// rounds buffered while it was away.
const (
	maxMetricsBytes      = 4 << 20
	MaxMetricsHostRows   = 120
	MaxMetricsContainers = 5000
	// MaxMetricsNames bounds the distinct container names an agent may
	// report within metricsNameWindow, so the rows it can create are
	// bounded by names × sample slots.
	MaxMetricsNames   = 1000
	metricsNameWindow = 24 * time.Hour
	// Accepted sample times after the clock correction: agents buffer at
	// most 30 minutes.
	metricsMaxAge  = 2 * time.Hour
	metricsMaxSkew = 5 * time.Minute
	// metricsSlot is the sampling interval; times are snapped to it.
	metricsSlot = 30
	// MetricsMinGap is the least time between two accepted batches of one
	// agent; an agent catching up waits longer than this between batches.
	MetricsMinGap    = 10 * time.Second
	metricsStoreMax  = 30 * time.Second
	metricsDeleteMax = 5 * time.Minute
)

// metricsIntake is the per-agent ingest state. It lives on the Service,
// keyed by agent id, so reconnecting doesn't reset it.
type metricsIntake struct {
	mu    sync.Mutex
	busy  bool
	last  time.Time
	names map[string]time.Time // last time each container name was accepted
}

func (s *Service) intake(agentID string) *metricsIntake {
	s.intakeMu.Lock()
	defer s.intakeMu.Unlock()
	if s.intakes == nil {
		s.intakes = map[string]*metricsIntake{}
	}
	in := s.intakes[agentID]
	if in == nil {
		in = &metricsIntake{names: map[string]time.Time{}}
		s.intakes[agentID] = in
	}
	return in
}

// begin reports whether a batch may be stored now: one at a time, at
// least MetricsMinGap after the previous one. done must follow a true.
func (in *metricsIntake) begin(now time.Time) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.busy || (!in.last.IsZero() && now.Sub(in.last) < MetricsMinGap) {
		return false
	}
	in.busy, in.last = true, now
	return true
}

func (in *metricsIntake) done() {
	in.mu.Lock()
	in.busy = false
	in.mu.Unlock()
}

// admit keeps the container samples whose name is known or still fits
// under MaxMetricsNames, and reports how many it dropped.
func (in *metricsIntake) admit(now time.Time, ctrs []MetricsContainerSample) ([]MetricsContainerSample, int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for n, seen := range in.names {
		if now.Sub(seen) > metricsNameWindow {
			delete(in.names, n)
		}
	}
	out := ctrs[:0]
	dropped := 0
	for _, c := range ctrs {
		if _, ok := in.names[c.Name]; !ok && len(in.names) >= MaxMetricsNames {
			dropped++
			continue
		}
		in.names[c.Name] = now
		out = append(out, c)
	}
	return out, dropped
}

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
// Only a skew beyond one slot is corrected, in whole slots, so transfer
// delay never moves two samples into the same slot.
func (b *MetricsBatch) Sanitize(now time.Time) {
	var shift int64
	if b.SentAt > 0 {
		if d := now.Unix() - b.SentAt; d > metricsSlot || d < -metricsSlot {
			shift = d / metricsSlot * metricsSlot
		}
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
	ctrs := b.Containers[:0]
	for _, c := range b.Containers {
		c.TS = fix(c.TS)
		if c.TS < lo || c.TS > hi || !containerName.MatchString(c.Name) || len(ctrs) >= MaxMetricsContainers {
			continue
		}
		ctrs = append(ctrs, c)
	}
	b.Containers = ctrs
}

// refuseMetrics answers a batch that arrived too early, while another one
// was being stored, or too large; the agent keeps it and retries. The
// answer is dropped rather than waited for when the send queue is full.
func (a *ConnectedAgent) refuseMetrics(f Frame) {
	if f.ID == "" {
		return
	}
	payload, _ := json.Marshal(ResponseEnvelope{OK: false, Error: "metrics batch refused, retry later"})
	select {
	case a.send <- Frame{Type: FrameRes, ID: f.ID, Payload: payload}:
	default:
	}
}

func (a *ConnectedAgent) answerMetrics(ctx context.Context, f Frame, err error) {
	if f.ID == "" {
		return
	}
	env := ResponseEnvelope{OK: true, Data: json.RawMessage(`{}`)}
	if err != nil {
		env = ResponseEnvelope{OK: false, Error: err.Error()}
	}
	payload, _ := json.Marshal(env)
	_ = a.enqueue(ctx, Frame{Type: FrameRes, ID: f.ID, Payload: payload})
}

// handleMetrics stores one batch and acknowledges it, so the agent keeps
// what was not stored. The host id is the agent's own, from its client
// certificate; nothing in the payload names a host. The caller has
// called begin on the agent's intake.
func (s *Service) handleMetrics(ctx context.Context, ag *ConnectedAgent, f Frame) {
	in := s.intake(ag.ID)
	defer in.done()
	ag.answerMetrics(ctx, f, s.storeMetrics(ctx, ag, in, f.Payload))
}

func (s *Service) storeMetrics(ctx context.Context, ag *ConnectedAgent, in *metricsIntake, payload json.RawMessage) error {
	if s.metricsSink == nil {
		return errors.New("history not accepted")
	}
	var b MetricsBatch
	if err := json.Unmarshal(payload, &b); err != nil {
		return err
	}
	now := time.Now()
	b.Sanitize(now)
	var dropped int
	b.Containers, dropped = in.admit(now, b.Containers)
	if dropped > 0 {
		slog.Warn("agent metrics: too many container names, samples dropped",
			"agent", ag.Name, "dropped", dropped, "limit", MaxMetricsNames)
	}
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

// deleteHistory removes the stored history of a deleted agent, in the
// background so a large history doesn't hold up the request. A batch
// still being stored is dropped by the sink, which checks that the agent
// exists.
func (s *Service) deleteHistory(id string) {
	s.intakeMu.Lock()
	delete(s.intakes, id)
	s.intakeMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), metricsDeleteMax)
		defer cancel()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			slog.Warn("agent delete: metrics history", "err", err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		for _, t := range metricsTables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE host_id = ?`, id); err != nil {
				slog.Warn("agent delete: metrics history", "table", t, "err", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			slog.Warn("agent delete: metrics history", "err", err)
		}
	}()
}
