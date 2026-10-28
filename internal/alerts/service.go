// Package alerts evaluates alert rules against recent metric samples
// and dispatches notifications on edge transitions (ok→firing,
// firing→resolved). Concept §3.2.
package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/notify"
)

var (
	ErrInvalidMetric    = errors.New("metric must be cpu_percent or mem_percent")
	ErrInvalidOperator  = errors.New(`operator must be "gt" (or ">") or "lt" (or "<")`)
	ErrInvalidThreshold = errors.New("threshold required")
	ErrInvalidKind      = errors.New("unknown rule kind")
	// ErrBuiltinImmutable is returned when a caller tries to delete a
	// built-in alert rule. Disabling and editing are still allowed; only
	// hard delete is blocked so new installs retain baseline coverage.
	ErrBuiltinImmutable = errors.New("built-in rule cannot be deleted; disable it instead")
)

// Rule is one row of alert_rules.
type Rule struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Kind selects the evaluator; "metric" is the CPU/memory threshold
	// rule, the others are listed in kinds.go.
	Kind            string     `json:"kind"`
	ContainerFilter string     `json:"container_filter"`
	ScopeHost       string     `json:"scope_host"`
	ScopeTag        string     `json:"scope_tag"`
	ScopeStack      string     `json:"scope_stack"`
	Metric          string     `json:"metric"`
	Operator        string     `json:"operator"`
	Threshold       float64    `json:"threshold"`
	DurationSeconds int        `json:"duration_seconds"`
	ChannelIDs      []int64    `json:"channel_ids"`
	Enabled         bool       `json:"enabled"`
	Severity        string     `json:"severity"`         // critical | warning | info
	CooldownSeconds int        `json:"cooldown_seconds"` // suppress re-notify for this long
	MutedUntil      *time.Time `json:"muted_until,omitempty"`
	// MuteSchedule is a JSON-encoded MuteSchedule (see schedule.go) for
	// recurring quiet-hours. When non-empty and IsMutedNow returns true
	// at fire time, notifications are suppressed (but firing state and
	// history still update so the UI shows the rule is breached).
	MuteSchedule string `json:"mute_schedule"`
	// Builtin rules ship with the server (P.11.5). The UI marks them
	// with a badge and disables the Delete action. Admins can still
	// disable, edit thresholds, and attach channels — only deletion is
	// blocked so new installs always retain baseline coverage.
	Builtin       bool       `json:"builtin"`
	FiringSince   *time.Time `json:"firing_since,omitempty"`
	LastTriggered *time.Time `json:"last_triggered_at,omitempty"`
	LastResolved  *time.Time `json:"last_resolved_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// HistoryEntry is one row of alert_history.
type HistoryEntry struct {
	ID            int64   `json:"id"`
	RuleID        int64   `json:"rule_id"`
	RuleName      string  `json:"rule_name"`
	Severity      string  `json:"severity,omitempty"` // enriched from alert_rules
	Kind          string  `json:"kind,omitempty"`     // enriched from alert_rules
	ContainerName string  `json:"container_name"`
	Status        string  `json:"status"`
	Message       string  `json:"message"`
	Value         float64 `json:"value"`
	Threshold     float64 `json:"threshold"`
	// Notified is false when the transition was recorded but channels
	// were not pinged (muted or inside the cooldown).
	Notified   bool      `json:"notified"`
	OccurredAt time.Time `json:"occurred_at"`
}

type RuleInput struct {
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	ContainerFilter string  `json:"container_filter"`
	ScopeHost       string  `json:"scope_host"`
	ScopeTag        string  `json:"scope_tag"`
	ScopeStack      string  `json:"scope_stack"`
	Metric          string  `json:"metric"`
	Operator        string  `json:"operator"`
	Threshold       float64 `json:"threshold"`
	DurationSeconds int     `json:"duration_seconds"`
	ChannelIDs      []int64 `json:"channel_ids"`
	Enabled         bool    `json:"enabled"`
	Severity        string  `json:"severity"`
	CooldownSeconds int     `json:"cooldown_seconds"`
	MutedUntil      string  `json:"muted_until,omitempty"` // ISO timestamp or empty
	MuteSchedule    string  `json:"mute_schedule,omitempty"`
}

// PromRecorder is the alerts-side hook into the prometheus collector.
// Decoupled via interface so main() can wire the concrete collector
// without the alerts package importing internal/metrics.
type PromRecorder interface {
	IncAlertFired(severity, rule string)
}

type Service struct {
	db     *sql.DB
	notify *notify.Service
	notifs *notifications.Service
	prom   PromRecorder

	interval time.Duration

	stop chan struct{}
	wg   sync.WaitGroup

	// firing maps "rule_id|container" to whether the fire was notified;
	// presence means the alert is active. lastNotified holds the time of
	// the last announced fire per key for cooldown.
	stateMu      sync.Mutex
	firing       map[string]bool
	lastNotified map[string]time.Time

	srcMu   sync.Mutex
	sources Sources
	notices *notices.Service

	rulesMu sync.Mutex
	rules   []Rule

	// Event state: kill/stop request times and recent dies per
	// container, and the running watcher per host.
	evMu        sync.Mutex
	stopping    map[string]time.Time
	dies        map[string][]time.Time
	watching    map[string]context.CancelFunc
	unsupported string

	lastRunID int64 // backup_runs high-water mark; -1 until first read
	certsAt   time.Time
	certsBusy atomic.Bool
	certs     atomic.Pointer[[]CertStatus]
}

// SetProm attaches a prom recorder after construction. Nil clears it.
func (s *Service) SetProm(p PromRecorder) { s.prom = p }

// SetNotifier wires in the bell-icon notification center. Optional —
// nil means "no notifications emitted by the alert engine", same as
// before. Set this in main.go after notifSvc is constructed.
func (s *Service) SetNotifier(n *notifications.Service) { s.notifs = n }

func NewService(db *sql.DB, notifier *notify.Service) *Service {
	return &Service{
		db:           db,
		notify:       notifier,
		interval:     DefaultSampleInterval,
		stop:         make(chan struct{}),
		firing:       make(map[string]bool),
		lastNotified: make(map[string]time.Time),
		stopping:     make(map[string]time.Time),
		dies:         make(map[string][]time.Time),
		watching:     make(map[string]context.CancelFunc),
		lastRunID:    -1,
	}
}

// Start launches the evaluator goroutine. Restores the firing state
// from alert_history first so a restart doesn't lose track of currently
// active alerts.
func (s *Service) Start(ctx context.Context) {
	if err := s.loadFiringState(ctx); err != nil {
		slog.Warn("alerts: load firing state", "err", err)
	}
	s.wg.Add(2)
	go s.evalLoop(ctx)
	go s.watchLoop(ctx)
	slog.Info("alerts evaluator started", "firing", len(s.firing))
}

// loadFiringState rebuilds the firing and cooldown state from
// alert_history: a key whose latest row is "fired" is still active.
func (s *Service) loadFiringState(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.rule_id, h.container_name, h.notified
		FROM alert_history h
		JOIN (
			SELECT rule_id, container_name, MAX(id) AS max_id
			FROM alert_history
			GROUP BY rule_id, container_name
		) latest ON h.id = latest.max_id
		JOIN alert_rules r ON r.id = h.rule_id
		WHERE h.status = 'fired' AND r.kind NOT IN ('oom', 'backup_failed')`)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for rows.Next() {
		var ruleID int64
		var name string
		var notified int
		if err := rows.Scan(&ruleID, &name, &notified); err != nil {
			rows.Close()
			return err
		}
		s.firing[stateKey(ruleID, name)] = notified == 1
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT rule_id, container_name, CAST(strftime('%s', MAX(occurred_at)) AS INTEGER)
		FROM alert_history
		WHERE status = 'fired' AND notified = 1
		GROUP BY rule_id, container_name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ruleID, unix int64
		var name string
		if err := rows.Scan(&ruleID, &name, &unix); err != nil {
			return err
		}
		s.lastNotified[stateKey(ruleID, name)] = time.Unix(unix, 0)
	}
	return rows.Err()
}

func (s *Service) Stop() {
	close(s.stop)
	s.wg.Wait()
}

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func (s *Service) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, container_filter, scope_host, scope_tag, scope_stack,
		       metric, operator, threshold,
		       duration_seconds, channel_ids, enabled,
		       severity, cooldown_seconds, muted_until, mute_schedule, builtin,
		       firing_since, last_triggered_at, last_resolved_at,
		       created_at, updated_at
		FROM alert_rules ORDER BY builtin DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Service) Create(ctx context.Context, in RuleInput) (*Rule, error) {
	in = normalizeInput(in)
	if err := validateInput(in); err != nil {
		return nil, err
	}
	ids, _ := json.Marshal(in.ChannelIDs)
	sev := in.Severity
	if sev == "" {
		sev = "warning"
	}
	cooldown := in.CooldownSeconds
	if cooldown <= 0 {
		cooldown = 300
	}
	var mutedUntil *time.Time
	if in.MutedUntil != "" {
		if t, err := time.Parse(time.RFC3339, in.MutedUntil); err == nil {
			mutedUntil = &t
		}
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_rules
			(name, kind, container_filter, scope_host, scope_tag, scope_stack, metric, operator, threshold,
			 duration_seconds, channel_ids, enabled, severity, cooldown_seconds, muted_until, mute_schedule)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Kind, in.ContainerFilter, in.ScopeHost, in.ScopeTag, in.ScopeStack, in.Metric, in.Operator, in.Threshold,
		in.DurationSeconds, string(ids), boolInt(in.Enabled), sev, cooldown, mutedUntil, in.MuteSchedule)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.getRule(ctx, id)
}

func (s *Service) Update(ctx context.Context, id int64, in RuleInput) (*Rule, error) {
	in = normalizeInput(in)
	if err := validateInput(in); err != nil {
		return nil, err
	}
	ids, _ := json.Marshal(in.ChannelIDs)
	sev := in.Severity
	if sev == "" {
		sev = "warning"
	}
	cooldown := in.CooldownSeconds
	if cooldown <= 0 {
		cooldown = 300
	}
	var mutedUntil *time.Time
	if in.MutedUntil != "" {
		if t, err := time.Parse(time.RFC3339, in.MutedUntil); err == nil {
			mutedUntil = &t
		}
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE alert_rules SET
			name = ?, kind = ?, container_filter = ?, scope_host = ?, scope_tag = ?, scope_stack = ?,
			metric = ?, operator = ?,
			threshold = ?, duration_seconds = ?, channel_ids = ?, enabled = ?,
			severity = ?, cooldown_seconds = ?, muted_until = ?, mute_schedule = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		in.Name, in.Kind, in.ContainerFilter, in.ScopeHost, in.ScopeTag, in.ScopeStack, in.Metric, in.Operator, in.Threshold,
		in.DurationSeconds, string(ids), boolInt(in.Enabled), sev, cooldown, mutedUntil, in.MuteSchedule, id); err != nil {
		return nil, err
	}
	return s.getRule(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	// Guard against deleting builtins so the UI and API share the same
	// rule. Builtins can still be disabled (Enabled=false) via Update.
	var builtin int
	err := s.db.QueryRowContext(ctx,
		`SELECT builtin FROM alert_rules WHERE id = ?`, id,
	).Scan(&builtin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // already gone, treat as success
	}
	if err != nil {
		return err
	}
	if builtin == 1 {
		return ErrBuiltinImmutable
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	return err
}

func (s *Service) History(ctx context.Context, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	// JOIN alert_rules so the UI can render severity-tinted history rows
	// without a second query. LEFT JOIN to keep history entries whose
	// rule has been deleted (rule_id may dangle).
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.id, h.rule_id, h.rule_name, COALESCE(r.severity, ''), COALESCE(r.kind, ''),
		       h.container_name, h.status, h.message,
		       COALESCE(h.value, 0), COALESCE(h.threshold, 0), h.notified, h.occurred_at
		  FROM alert_history h
		  LEFT JOIN alert_rules r ON r.id = h.rule_id
		 ORDER BY h.occurred_at DESC, h.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		var notified int
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Severity, &e.Kind,
			&e.ContainerName, &e.Status, &e.Message, &e.Value, &e.Threshold,
			&notified, &e.OccurredAt); err != nil {
			return nil, err
		}
		e.Notified = notified == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// RuleStats summarises recent activity for a single rule over a window
// of days. fires_count is the number of "fired" history rows in that
// window. last_fired is the most recent occurred_at, nil if quiet.
type RuleStats struct {
	RuleID     int64      `json:"rule_id"`
	WindowDays int        `json:"window_days"`
	FiresCount int        `json:"fires_count"`
	LastFired  *time.Time `json:"last_fired,omitempty"`
}

// RuleStatsByID returns activity stats over the last `days` for the
// given rule. days <= 0 falls back to 7.
func (s *Service) RuleStatsByID(ctx context.Context, ruleID int64, days int) (*RuleStats, error) {
	if days <= 0 {
		days = 7
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	out := &RuleStats{RuleID: ruleID, WindowDays: days}
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), CAST(strftime('%s', MAX(occurred_at)) AS INTEGER)
		  FROM alert_history
		 WHERE rule_id = ? AND status = 'fired' AND occurred_at >= ?`,
		ruleID, cutoff.UTC().Format("2006-01-02 15:04:05"))
	var lastFired sql.NullInt64
	if err := row.Scan(&out.FiresCount, &lastFired); err != nil {
		return nil, err
	}
	if lastFired.Valid {
		t := time.Unix(lastFired.Int64, 0)
		out.LastFired = &t
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Evaluator
// -----------------------------------------------------------------------------

func (s *Service) evalLoop(ctx context.Context) {
	defer s.wg.Done()
	// First eval slightly delayed so the metrics collector has time to
	// produce at least one sample.
	timer := time.NewTimer(35 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-timer.C:
			s.evalAll(ctx, time.Now())
			timer.Reset(s.interval)
		}
	}
}

func (s *Service) evalAll(ctx context.Context, now time.Time) {
	rules, err := s.ListRules(ctx)
	if err != nil {
		slog.Warn("alerts list rules", "err", err)
		return
	}
	s.setRules(rules)
	s.refreshCerts(ctx, now)

	src := s.src()
	var mounts []HostMounts
	mountsLoaded := false
	runs, runsErr := s.newFailedRuns(ctx)
	if runsErr != nil {
		slog.Warn("alerts: read backup runs", "err", runsErr)
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		var err error
		switch r.Kind {
		case KindMetric:
			err = s.evalRule(ctx, &r, now)
		case KindRestartLoop:
			s.expireRestartLoops(ctx, &r, now)
		case KindHostOffline:
			err = s.evalHostOffline(ctx, &r, now)
		case KindDiskUsage:
			if !mountsLoaded && src.Mounts != nil {
				mounts, mountsLoaded = src.Mounts(ctx), true
			}
			s.evalDisk(ctx, &r, mounts, now)
		case KindBackupFailed:
			s.evalBackupFailed(ctx, &r, runs, now)
		case KindBackupMissed:
			err = s.evalBackupMissed(ctx, &r, now)
		case KindCertExpiry:
			s.evalCerts(ctx, &r, now)
		}
		if err != nil {
			slog.Warn("alerts eval", "rule", r.Name, "err", err)
		}
	}
}

func metricExpr(metric string) (string, error) {
	switch metric {
	case "cpu_percent":
		return "cpu_percent", nil
	case "mem_percent":
		return "CASE WHEN mem_limit > 0 THEN (mem_used * 100.0 / mem_limit) ELSE 0 END", nil
	}
	return "", ErrInvalidMetric
}

// evalRule loads the recent sample series per matching container and
// lets decide() compute the edge. Containers that are firing but have
// no samples left in the lookback window are resolved.
func (s *Service) evalRule(ctx context.Context, r *Rule, now time.Time) error {
	col, err := metricExpr(r.Metric)
	if err != nil {
		return err
	}
	if r.Operator != "gt" && r.Operator != "lt" {
		return ErrInvalidOperator
	}
	duration := time.Duration(r.DurationSeconds) * time.Second
	lookback := duration
	if lookback < s.interval {
		lookback = s.interval
	}
	lookback += staleFactor * s.interval

	whereName := ""
	args := []any{now.Add(-lookback).Unix(), now.Unix()}
	if r.ContainerFilter != "" && r.ContainerFilter != "*" {
		whereName = " AND container_name = ?"
		args = append(args, r.ContainerFilter)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT container_name, ts, %s
		  FROM metrics_raw
		 WHERE ts >= ? AND ts <= ?%s
		 ORDER BY container_name, ts`, col, whereName), args...)
	if err != nil {
		return err
	}
	series := map[string][]metricSample{}
	var order []string
	for rows.Next() {
		var name string
		var smp metricSample
		if err := rows.Scan(&name, &smp.TS, &smp.Value); err != nil {
			rows.Close()
			return err
		}
		if _, ok := series[name]; !ok {
			order = append(order, name)
		}
		series[name] = append(series[name], smp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, name := range order {
		smp := series[name]
		key := stateKey(r.ID, name)
		wasFiring := s.isFiring(key)
		firing := decide(smp, r.Operator, r.Threshold, duration, wasFiring, now, s.interval)
		last := smp[len(smp)-1].Value
		switch {
		case firing && !wasFiring:
			s.fireRule(ctx, r, name, fmt.Sprintf("%s on %s: %s = %.1f (threshold %s %.1f)",
				r.Name, name, r.Metric, last, r.Operator, r.Threshold), last, now)
		case !firing && wasFiring:
			s.resolveRule(ctx, r, name, fmt.Sprintf("%s on %s resolved: %s = %.1f",
				r.Name, name, r.Metric, last), last, now)
		}
	}

	prefix := fmt.Sprintf("%d|", r.ID)
	var gone []string
	s.stateMu.Lock()
	for key := range s.firing {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if name := strings.TrimPrefix(key, prefix); series[name] == nil {
			gone = append(gone, name)
		}
	}
	s.stateMu.Unlock()
	for _, name := range gone {
		s.resolveRule(ctx, r, name, fmt.Sprintf("%s on %s resolved: no samples", r.Name, name), 0, now)
	}
	return nil
}

// mutedAt reports whether notifications for r are suppressed at t,
// either by the one-shot muted_until or by the recurring schedule.
func (r *Rule) mutedAt(t time.Time) bool {
	if r.MutedUntil != nil && t.Before(*r.MutedUntil) {
		return true
	}
	return IsMutedNow(r.MuteSchedule, t)
}

// inCooldown reports whether a fire notification for key went out less
// than cooldown_seconds ago. Suppressed fires don't count, so a flapping
// container is still announced once per cooldown period.
func (s *Service) inCooldown(r *Rule, key string, t time.Time) bool {
	if r.CooldownSeconds <= 0 {
		return false
	}
	s.stateMu.Lock()
	last, ok := s.lastNotified[key]
	s.stateMu.Unlock()
	return ok && t.Sub(last) < time.Duration(r.CooldownSeconds)*time.Second
}

// fireRule records a fire for target and notifies unless muted or in
// cooldown. One-shot kinds (OOM, failed backup run) have nothing to
// resolve, so they never enter the firing set.
func (s *Service) fireRule(ctx context.Context, r *Rule, container, msg string, value float64, ts time.Time) {
	key := stateKey(r.ID, container)
	muted := r.mutedAt(ts)
	cooling := s.inCooldown(r, key, ts)
	notified := !muted && !cooling

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, value, threshold, notified)
		VALUES (?, ?, ?, 'fired', ?, ?, ?, ?)`,
		r.ID, r.Name, container, msg, value, r.Threshold, boolInt(notified)); err != nil {
		slog.Warn("alerts: record fire", "rule", r.Name, "err", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE alert_rules SET firing_since = COALESCE(firing_since, ?), last_triggered_at = ? WHERE id = ?`,
		nullIf(oneShot(r.Kind), ts), ts, r.ID); err != nil {
		slog.Warn("alerts: update rule state", "rule", r.Name, "err", err)
	}
	s.stateMu.Lock()
	if !oneShot(r.Kind) {
		s.firing[key] = notified
	}
	if notified {
		s.lastNotified[key] = ts
	}
	s.stateMu.Unlock()

	slog.Info("alert fired", "rule", r.Name, "container", container, "value", value,
		"muted", muted, "cooldown", cooling)
	if s.prom != nil {
		s.prom.IncAlertFired(r.Severity, r.Name)
	}
	if !notified {
		return
	}

	if s.notify != nil && len(r.ChannelIDs) > 0 {
		level := notify.LevelWarning
		switch {
		case r.Severity == "critical", r.Kind == KindMetric && r.Operator == "gt" && value >= r.Threshold*1.5:
			level = notify.LevelCritical
		case r.Severity == "info":
			level = notify.LevelInfo
		}
		s.notify.SendToAll(ctx, r.ChannelIDs, notify.Notification{
			Title:     "🔥 " + r.Name,
			Body:      msg,
			Level:     level,
			Container: container,
			Metric:    r.Metric,
			Value:     value,
			Threshold: r.Threshold,
			Time:      ts,
		})
	}

	// Bell-icon feed — independent of the channel-list. Even when the
	// rule has no external channels set, operators want a durable trail
	// in the UI.
	if s.notifs != nil {
		sev := notifications.SevWarning
		if r.Severity == "critical" {
			sev = notifications.SevError
		} else if r.Severity == "info" {
			sev = notifications.SevInfo
		}
		_, _ = s.notifs.Emit(ctx, notifications.EmitInput{
			Kind:     notifications.KindAlertFire,
			Severity: sev,
			Title:    "Alert firing: " + r.Name,
			Body:     msg,
			Link:     "/alerts",
		})
	}
}

// resolveRule records the resolve and notifies only when the matching
// fire was announced and the rule isn't muted now — a "resolved" for an
// alert nobody was told about is noise.
func (s *Service) resolveRule(ctx context.Context, r *Rule, container, msg string, value float64, ts time.Time) {
	key := stateKey(r.ID, container)
	s.stateMu.Lock()
	fireNotified := s.firing[key]
	delete(s.firing, key)
	s.stateMu.Unlock()
	notified := fireNotified && !r.mutedAt(ts)

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, value, threshold, notified)
		VALUES (?, ?, ?, 'resolved', ?, ?, ?, ?)`,
		r.ID, r.Name, container, msg, value, r.Threshold, boolInt(notified)); err != nil {
		slog.Warn("alerts: record resolve", "rule", r.Name, "err", err)
	}
	if !s.ruleHasFiring(r.ID) {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE alert_rules SET firing_since = NULL, last_resolved_at = ? WHERE id = ?`,
			ts, r.ID); err != nil {
			slog.Warn("alerts: update rule state", "rule", r.Name, "err", err)
		}
	}
	slog.Info("alert resolved", "rule", r.Name, "container", container, "value", value, "notified", notified)

	if notified && s.notify != nil && len(r.ChannelIDs) > 0 {
		s.notify.SendToAll(ctx, r.ChannelIDs, notify.Notification{
			Title:     "✅ " + r.Name + " resolved",
			Body:      msg,
			Level:     notify.LevelInfo,
			Container: container,
			Metric:    r.Metric,
			Value:     value,
			Threshold: r.Threshold,
			Time:      ts,
		})
	}
}

func (s *Service) isFiring(key string) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	_, ok := s.firing[key]
	return ok
}

func (s *Service) ruleHasFiring(ruleID int64) bool {
	prefix := fmt.Sprintf("%d|", ruleID)
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for key := range s.firing {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func (s *Service) getRule(ctx context.Context, id int64) (*Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, kind, container_filter, scope_host, scope_tag, scope_stack,
		       metric, operator, threshold,
		       duration_seconds, channel_ids, enabled,
		       severity, cooldown_seconds, muted_until, mute_schedule, builtin,
		       firing_since, last_triggered_at, last_resolved_at,
		       created_at, updated_at
		FROM alert_rules WHERE id = ?`, id)
	return scanRule(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRule(r rowScanner) (*Rule, error) {
	var rule Rule
	var ids string
	var enabled, builtin int
	var severity sql.NullString
	var mutedUntil sql.NullTime
	var muteSchedule sql.NullString
	var firingSince, lastT, lastR sql.NullTime
	if err := r.Scan(
		&rule.ID, &rule.Name, &rule.Kind, &rule.ContainerFilter,
		&rule.ScopeHost, &rule.ScopeTag, &rule.ScopeStack, &rule.Metric, &rule.Operator,
		&rule.Threshold, &rule.DurationSeconds, &ids, &enabled,
		&severity, &rule.CooldownSeconds, &mutedUntil, &muteSchedule, &builtin,
		&firingSince, &lastT, &lastR, &rule.CreatedAt, &rule.UpdatedAt,
	); err != nil {
		return nil, err
	}
	rule.MuteSchedule = muteSchedule.String
	rule.Enabled = enabled == 1
	rule.Builtin = builtin == 1
	rule.Severity = severity.String
	if rule.Severity == "" {
		rule.Severity = "warning"
	}
	_ = json.Unmarshal([]byte(ids), &rule.ChannelIDs)
	if mutedUntil.Valid {
		t := mutedUntil.Time
		rule.MutedUntil = &t
	}
	if firingSince.Valid {
		t := firingSince.Time
		rule.FiringSince = &t
	}
	if lastT.Valid {
		t := lastT.Time
		rule.LastTriggered = &t
	}
	if lastR.Valid {
		t := lastR.Time
		rule.LastResolved = &t
	}
	return &rule, nil
}

// normalizeOperator accepts common symbol aliases (">", "<", ">=", "<=")
// and folds them to the canonical "gt"/"lt" used internally.
func normalizeOperator(op string) string {
	switch op {
	case ">", ">=", "gt", "GT":
		return "gt"
	case "<", "<=", "lt", "LT":
		return "lt"
	}
	return op
}

// normalizeInput folds operator aliases and defaults the kind and the
// target filter.
func normalizeInput(in RuleInput) RuleInput {
	if in.Kind == "" {
		in.Kind = KindMetric
	}
	in.Operator = normalizeOperator(in.Operator)
	if in.ContainerFilter == "" && in.Kind != KindMetric {
		in.ContainerFilter = "*"
	}
	return in
}

func validateInput(in RuleInput) error {
	if in.Name == "" {
		return errors.New("name required")
	}
	if in.DurationSeconds < 0 {
		return errors.New("duration_seconds must be >= 0")
	}
	if in.ContainerFilter == "" {
		return errors.New("container_filter required (use '*' for all)")
	}
	switch in.Kind {
	case KindMetric:
		if in.Metric != "cpu_percent" && in.Metric != "mem_percent" {
			return ErrInvalidMetric
		}
		if in.Operator != "gt" && in.Operator != "lt" {
			return ErrInvalidOperator
		}
		if in.Threshold == 0 {
			return ErrInvalidThreshold
		}
	case KindRestartLoop:
		if in.Threshold < 2 || in.DurationSeconds <= 0 {
			return errors.New("restart loop needs threshold >= 2 restarts and duration_seconds > 0")
		}
	case KindDiskUsage:
		if in.Threshold <= 0 || in.Threshold > 100 {
			return errors.New("disk usage threshold must be a percentage between 0 and 100")
		}
	case KindCertExpiry:
		if in.Threshold <= 0 {
			return errors.New("certificate expiry threshold must be a number of days > 0")
		}
	case KindContainerStopped, KindUnhealthy, KindOOM, KindHostOffline,
		KindBackupFailed, KindBackupMissed:
	default:
		return ErrInvalidKind
	}
	return nil
}

func stateKey(ruleID int64, container string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s", ruleID, container)
	return b.String()
}

func nullIf(cond bool, v any) any {
	if cond {
		return nil
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
