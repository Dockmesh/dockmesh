// P.11.9 — Prometheus collectors.
//
// All metrics live on a private registry (not the global default) so
// we stay in control of what gets exposed and can add custom Go /
// process collectors without fighting library-default auto-registration.
// Exposed via GET /metrics at the router root.
package metrics

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// PromMetrics holds every collector we expose. One instance per server
// process. Use the helper methods (IncAPIRequest, ObserveDeploy, …)
// from instrumentation points; direct collector access is reserved for
// the gauge refresher which needs Reset semantics.
type PromMetrics struct {
	Registry *prometheus.Registry

	HostsTotal             *prometheus.GaugeVec
	StacksTotal            *prometheus.GaugeVec
	ContainersTotal        *prometheus.GaugeVec
	AgentLastSeenSeconds   *prometheus.GaugeVec
	BackupLastRunTimestamp *prometheus.GaugeVec
	BackupLastDurationSec  *prometheus.GaugeVec

	DeployDurationSec *prometheus.HistogramVec
	AgentRTTSec       *prometheus.HistogramVec

	APIRequestsTotal  *prometheus.CounterVec
	AuditEntriesTotal *prometheus.CounterVec
	AlertsFiredTotal  *prometheus.CounterVec

	db *sql.DB
}

// NewPromMetrics builds the collectors and registers them on a fresh
// registry. Go runtime + process collectors are included via the
// standard helpers so scrapes get go_goroutines, process_cpu_seconds,
// etc. for free.
func NewPromMetrics(db *sql.DB) *PromMetrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := &PromMetrics{
		Registry: reg,
		db:       db,

		HostsTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dockmesh_hosts_total",
			Help: "Number of known hosts grouped by status (local/online/offline/revoked).",
		}, []string{"status"}),

		StacksTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dockmesh_stacks_total",
			Help: "Number of stacks known to Dockmesh.",
		}, []string{"host"}),

		ContainersTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dockmesh_containers_total",
			Help: "Number of containers per host and state.",
		}, []string{"host", "state"}),

		AgentLastSeenSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dockmesh_agent_last_seen_seconds",
			Help: "Seconds since last heartbeat from each agent. High value = probably offline.",
		}, []string{"agent", "host"}),

		BackupLastRunTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dockmesh_backup_last_run_timestamp_seconds",
			Help: "Unix timestamp of the most recent successful run of each backup job.",
		}, []string{"job"}),

		BackupLastDurationSec: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dockmesh_backup_last_duration_seconds",
			Help: "Duration of the most recent run of each backup job.",
		}, []string{"job"}),

		DeployDurationSec: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dockmesh_deploy_duration_seconds",
			Help:    "How long a stack deploy took, labelled by stack and result (ok/error).",
			Buckets: prometheus.DefBuckets,
		}, []string{"stack", "result"}),

		AgentRTTSec: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dockmesh_agent_rtt_seconds",
			Help:    "Round-trip time of agent requests.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
		}, []string{"agent"}),

		APIRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dockmesh_api_requests_total",
			Help: "HTTP API requests counted by method, path pattern and status.",
		}, []string{"method", "path", "status"}),

		AuditEntriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dockmesh_audit_entries_total",
			Help: "Audit log entries written since start, labelled by action.",
		}, []string{"action"}),

		AlertsFiredTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dockmesh_alerts_fired_total",
			Help: "Count of alert rules transitioning to firing state.",
		}, []string{"severity", "rule"}),
	}
	reg.MustRegister(
		m.HostsTotal, m.StacksTotal, m.ContainersTotal,
		m.AgentLastSeenSeconds, m.BackupLastRunTimestamp, m.BackupLastDurationSec,
		m.DeployDurationSec, m.AgentRTTSec,
		m.APIRequestsTotal, m.AuditEntriesTotal, m.AlertsFiredTotal,
	)
	return m
}

// StartRefresher launches a background goroutine that refreshes the
// gauge families from the DB every 30s. Counters + histograms don't
// need refreshing (they're event-driven); gauges do because a scrape
// shouldn't force a fresh DB query of its own.
func (m *PromMetrics) StartRefresher(ctx context.Context) {
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		m.refresh(ctx) // immediate fill so the first scrape isn't empty
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				m.refresh(ctx)
			}
		}
	}()
}

func (m *PromMetrics) refresh(ctx context.Context) {
	if m.db == nil {
		return
	}
	// Reset gauges before re-filling so a dropped label (e.g. removed
	// agent) doesn't leave a stale sample forever.
	m.HostsTotal.Reset()
	m.StacksTotal.Reset()
	m.ContainersTotal.Reset()
	m.AgentLastSeenSeconds.Reset()
	m.BackupLastRunTimestamp.Reset()
	m.BackupLastDurationSec.Reset()

	// Hosts — count agents by status + 1 local.
	m.HostsTotal.WithLabelValues("local").Set(1)
	rows, err := m.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM agents GROUP BY status`)
	if err == nil {
		for rows.Next() {
			var status string
			var n int
			if err := rows.Scan(&status, &n); err == nil {
				m.HostsTotal.WithLabelValues(status).Set(float64(n))
			}
		}
		rows.Close()
	} else {
		slog.Debug("prom refresh: hosts", "err", err)
	}

	// Stacks per-host. deployments gives us the host_id dimension;
	// a stack that isn't deployed anywhere is still counted under
	// the literal label "none" so operators see drift.
	rows, err = m.db.QueryContext(ctx, `
		SELECT COALESCE(host_id, 'none'), COUNT(*)
		  FROM deployments
		 GROUP BY host_id`)
	if err == nil {
		for rows.Next() {
			var host string
			var n int
			if err := rows.Scan(&host, &n); err == nil {
				m.StacksTotal.WithLabelValues(host).Set(float64(n))
			}
		}
		rows.Close()
	}

	// Agent heartbeat age. last_seen_at NULL = never seen yet;
	// we skip those to keep cardinality honest.
	rows, err = m.db.QueryContext(ctx, `
		SELECT id, name, last_seen_at FROM agents WHERE last_seen_at IS NOT NULL`)
	if err == nil {
		for rows.Next() {
			var id, name string
			var seen time.Time
			if err := rows.Scan(&id, &name, &seen); err == nil {
				age := time.Since(seen).Seconds()
				m.AgentLastSeenSeconds.WithLabelValues(name, id).Set(age)
			}
		}
		rows.Close()
	}

	// Backup job timestamps — last run per job.
	rows, err = m.db.QueryContext(ctx, `
		SELECT j.name, r.finished_at, r.duration_ms
		  FROM backup_jobs j
		  LEFT JOIN backup_runs r ON r.id = (
		    SELECT id FROM backup_runs
		     WHERE job_id = j.id AND status = 'success'
		     ORDER BY finished_at DESC LIMIT 1)`)
	if err == nil {
		for rows.Next() {
			var name string
			var finished *time.Time
			var durMs *int64
			if err := rows.Scan(&name, &finished, &durMs); err == nil && finished != nil {
				m.BackupLastRunTimestamp.WithLabelValues(name).Set(float64(finished.Unix()))
				if durMs != nil {
					m.BackupLastDurationSec.WithLabelValues(name).Set(float64(*durMs) / 1000.0)
				}
			}
		}
		rows.Close()
	}
}

// -----------------------------------------------------------------------------
// Event-driven helpers — call these from instrumentation points.
// -----------------------------------------------------------------------------

// IncAPIRequest is called by the API logging middleware on every
// completed request.
func (m *PromMetrics) IncAPIRequest(method, pathPattern, status string) {
	if m == nil {
		return
	}
	m.APIRequestsTotal.WithLabelValues(method, pathPattern, status).Inc()
}

// IncAuditEntry is called by the audit service after a successful
// insert.
func (m *PromMetrics) IncAuditEntry(action string) {
	if m == nil {
		return
	}
	m.AuditEntriesTotal.WithLabelValues(action).Inc()
}

// IncAlertFired is called by the alerts evaluator when a rule
// transitions to firing state.
func (m *PromMetrics) IncAlertFired(severity, rule string) {
	if m == nil {
		return
	}
	m.AlertsFiredTotal.WithLabelValues(severity, rule).Inc()
}

// ObserveDeploy is called by the compose deploy path on completion.
func (m *PromMetrics) ObserveDeploy(stack, result string, dur time.Duration) {
	if m == nil {
		return
	}
	m.DeployDurationSec.WithLabelValues(stack, result).Observe(dur.Seconds())
}

// ObserveAgentRTT is called by the agent client after each round-trip.
func (m *PromMetrics) ObserveAgentRTT(agent string, dur time.Duration) {
	if m == nil {
		return
	}
	m.AgentRTTSec.WithLabelValues(agent).Observe(dur.Seconds())
}
