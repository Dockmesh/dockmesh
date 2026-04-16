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
	"time"

	"github.com/dockmesh/dockmesh/internal/notify"
)

var (
	ErrInvalidMetric    = errors.New("metric must be cpu_percent or mem_percent")
	ErrInvalidOperator  = errors.New("operator must be gt or lt")
	ErrInvalidThreshold = errors.New("threshold required")
)

// Rule is one row of alert_rules.
type Rule struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	ContainerFilter string     `json:"container_filter"`
	Metric          string     `json:"metric"`
	Operator        string     `json:"operator"`
	Threshold       float64    `json:"threshold"`
	DurationSeconds int        `json:"duration_seconds"`
	ChannelIDs      []int64    `json:"channel_ids"`
	Enabled         bool       `json:"enabled"`
	Severity        string     `json:"severity"`          // critical | warning | info
	CooldownSeconds int        `json:"cooldown_seconds"`  // suppress re-notify for this long
	MutedUntil      *time.Time `json:"muted_until,omitempty"`
	FiringSince     *time.Time `json:"firing_since,omitempty"`
	LastTriggered   *time.Time `json:"last_triggered_at,omitempty"`
	LastResolved    *time.Time `json:"last_resolved_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// HistoryEntry is one row of alert_history.
type HistoryEntry struct {
	ID            int64     `json:"id"`
	RuleID        int64     `json:"rule_id"`
	RuleName      string    `json:"rule_name"`
	ContainerName string    `json:"container_name"`
	Status        string    `json:"status"`
	Message       string    `json:"message"`
	Value         float64   `json:"value"`
	Threshold     float64   `json:"threshold"`
	OccurredAt    time.Time `json:"occurred_at"`
}

type RuleInput struct {
	Name            string  `json:"name"`
	ContainerFilter string  `json:"container_filter"`
	Metric          string  `json:"metric"`
	Operator        string  `json:"operator"`
	Threshold       float64 `json:"threshold"`
	DurationSeconds int     `json:"duration_seconds"`
	ChannelIDs      []int64 `json:"channel_ids"`
	Enabled         bool    `json:"enabled"`
	Severity        string  `json:"severity"`
	CooldownSeconds int     `json:"cooldown_seconds"`
	MutedUntil      string  `json:"muted_until,omitempty"` // ISO timestamp or empty
}

type Service struct {
	db     *sql.DB
	notify *notify.Service

	stop chan struct{}
	wg   sync.WaitGroup

	// In-memory firing state per (rule_id, container_name) to avoid
	// re-notifying every 30 seconds while a breach persists.
	stateMu sync.Mutex
	firing  map[string]bool
}

func NewService(db *sql.DB, notifier *notify.Service) *Service {
	return &Service{
		db:     db,
		notify: notifier,
		stop:   make(chan struct{}),
		firing: make(map[string]bool),
	}
}

// Start launches the evaluator goroutine. Restores the firing state
// from alert_history first so a restart doesn't lose track of currently
// active alerts.
func (s *Service) Start(ctx context.Context) {
	if err := s.loadFiringState(ctx); err != nil {
		slog.Warn("alerts: load firing state", "err", err)
	}
	s.wg.Add(1)
	go s.evalLoop(ctx)
	slog.Info("alerts evaluator started", "firing", len(s.firing))
}

// loadFiringState rebuilds s.firing from the latest alert_history row
// per (rule_id, container_name). Anything whose latest status is "fired"
// is still considered firing — the next eval will re-resolve it if the
// underlying metric has recovered.
func (s *Service) loadFiringState(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.rule_id, h.container_name
		FROM alert_history h
		JOIN (
			SELECT rule_id, container_name, MAX(id) AS max_id
			FROM alert_history
			GROUP BY rule_id, container_name
		) latest ON h.id = latest.max_id
		WHERE h.status = 'fired'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for rows.Next() {
		var ruleID int64
		var name string
		if err := rows.Scan(&ruleID, &name); err != nil {
			return err
		}
		s.firing[stateKey(ruleID, name)] = true
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
		SELECT id, name, container_filter, metric, operator, threshold,
		       duration_seconds, channel_ids, enabled,
		       severity, cooldown_seconds, muted_until,
		       firing_since, last_triggered_at, last_resolved_at,
		       created_at, updated_at
		FROM alert_rules ORDER BY id DESC`)
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
			(name, container_filter, metric, operator, threshold,
			 duration_seconds, channel_ids, enabled, severity, cooldown_seconds, muted_until)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.ContainerFilter, in.Metric, in.Operator, in.Threshold,
		in.DurationSeconds, string(ids), boolInt(in.Enabled), sev, cooldown, mutedUntil)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.getRule(ctx, id)
}

func (s *Service) Update(ctx context.Context, id int64, in RuleInput) (*Rule, error) {
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
			name = ?, container_filter = ?, metric = ?, operator = ?,
			threshold = ?, duration_seconds = ?, channel_ids = ?, enabled = ?,
			severity = ?, cooldown_seconds = ?, muted_until = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		in.Name, in.ContainerFilter, in.Metric, in.Operator, in.Threshold,
		in.DurationSeconds, string(ids), boolInt(in.Enabled), sev, cooldown, mutedUntil, id); err != nil {
		return nil, err
	}
	return s.getRule(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	return err
}

func (s *Service) History(ctx context.Context, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, rule_id, rule_name, container_name, status, message,
		       COALESCE(value, 0), COALESCE(threshold, 0), occurred_at
		FROM alert_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.ContainerName, &e.Status,
			&e.Message, &e.Value, &e.Threshold, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
			s.evalAll(ctx)
			timer.Reset(30 * time.Second)
		}
	}
}

func (s *Service) evalAll(ctx context.Context) {
	rules, err := s.ListRules(ctx)
	if err != nil {
		slog.Warn("alerts list rules", "err", err)
		return
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if err := s.evalRule(ctx, &r); err != nil {
			slog.Warn("alerts eval", "rule", r.Name, "err", err)
		}
	}
}

// evalRule queries the recent samples that fall inside the rule's
// duration window and decides whether the rule is currently firing.
//
// The firing condition: every sample in the window must satisfy the
// operator+threshold, so a single recovery sample is enough to clear
// the alert. This is "all-of" semantics, the simplest interpretation
// of "must persist for N seconds".
//
// Resolution: containers that were firing but now have either a
// non-breaching sample OR no samples for 2× the collection interval
// (i.e. the container was removed) are resolved.
func (s *Service) evalRule(ctx context.Context, r *Rule) error {
	now := time.Now()
	from := now.Add(-time.Duration(r.DurationSeconds) * time.Second).Unix()
	seen := make(map[string]bool)

	// Resolve the metric expression to a SQL expression on metrics_raw.
	var col string
	switch r.Metric {
	case "cpu_percent":
		col = "cpu_percent"
	case "mem_percent":
		// computed on the fly — protect against div by zero
		col = "CASE WHEN mem_limit > 0 THEN (mem_used * 100.0 / mem_limit) ELSE 0 END"
	default:
		return ErrInvalidMetric
	}

	// Per-container aggregation: min and max over the window, plus the
	// most recent sample so we can include "value" in the notification.
	var op string
	switch r.Operator {
	case "gt":
		op = ">"
	case "lt":
		op = "<"
	default:
		return ErrInvalidOperator
	}

	// Container filter: "*" → all, else exact name match.
	whereName := ""
	args := []any{from}
	if r.ContainerFilter != "" && r.ContainerFilter != "*" {
		whereName = " AND container_name = ?"
		args = append(args, r.ContainerFilter)
	}

	// We pull (container, min, max, sample_count, last_value) per container.
	// "all samples breach" means: for op=gt, min > threshold; for op=lt, max < threshold.
	q := fmt.Sprintf(`
		SELECT container_name,
		       MIN(%s) AS min_val,
		       MAX(%s) AS max_val,
		       COUNT(*) AS n,
		       (SELECT %s FROM metrics_raw m2 WHERE m2.container_name = m.container_name ORDER BY ts DESC LIMIT 1) AS last_val
		FROM metrics_raw m
		WHERE ts >= ?%s
		GROUP BY container_name`, col, col, col, whereName)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	now2 := now
	for rows.Next() {
		var name string
		var minV, maxV, lastV float64
		var n int
		if err := rows.Scan(&name, &minV, &maxV, &n, &lastV); err != nil {
			return err
		}
		if n < 1 {
			continue
		}
		seen[name] = true
		breach := false
		switch op {
		case ">":
			breach = minV > r.Threshold
		case "<":
			breach = maxV < r.Threshold
		}

		key := stateKey(r.ID, name)
		s.stateMu.Lock()
		wasFiring := s.firing[key]
		s.stateMu.Unlock()

		if breach && !wasFiring {
			s.fireRule(ctx, r, name, lastV, now2)
			s.setFiring(key, true)
		} else if !breach && wasFiring {
			s.resolveRule(ctx, r, name, lastV, now2)
			s.setFiring(key, false)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Resolve currently-firing containers that produced no samples in
	// this window — usually because the container was stopped or removed.
	s.stateMu.Lock()
	prefix := fmt.Sprintf("%d|", r.ID)
	var stale []string
	for key := range s.firing {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		name := strings.TrimPrefix(key, prefix)
		if seen[name] {
			continue
		}
		stale = append(stale, name)
	}
	s.stateMu.Unlock()

	for _, name := range stale {
		s.resolveRule(ctx, r, name, 0, now2)
		s.setFiring(stateKey(r.ID, name), false)
	}
	return nil
}

func (s *Service) fireRule(ctx context.Context, r *Rule, container string, value float64, ts time.Time) {
	msg := fmt.Sprintf("%s on %s — %s = %.1f (threshold %s %.1f)",
		r.Name, container, r.Metric, value, r.Operator, r.Threshold)
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, value, threshold)
		VALUES (?, ?, ?, 'fired', ?, ?, ?)`,
		r.ID, r.Name, container, msg, value, r.Threshold)
	_, _ = s.db.ExecContext(ctx,
		`UPDATE alert_rules SET firing_since = ?, last_triggered_at = ? WHERE id = ?`,
		ts, ts, r.ID)
	slog.Info("alert fired", "rule", r.Name, "container", container, "value", value)

	if s.notify != nil && len(r.ChannelIDs) > 0 {
		level := notify.LevelWarning
		if r.Operator == "gt" && value >= r.Threshold*1.5 {
			level = notify.LevelCritical
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
}

func (s *Service) resolveRule(ctx context.Context, r *Rule, container string, value float64, ts time.Time) {
	msg := fmt.Sprintf("%s on %s resolved — %s = %.1f", r.Name, container, r.Metric, value)
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, value, threshold)
		VALUES (?, ?, ?, 'resolved', ?, ?, ?)`,
		r.ID, r.Name, container, msg, value, r.Threshold)
	_, _ = s.db.ExecContext(ctx,
		`UPDATE alert_rules SET firing_since = NULL, last_resolved_at = ? WHERE id = ?`,
		ts, r.ID)
	slog.Info("alert resolved", "rule", r.Name, "container", container, "value", value)

	if s.notify != nil && len(r.ChannelIDs) > 0 {
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

func (s *Service) setFiring(key string, firing bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if firing {
		s.firing[key] = true
	} else {
		delete(s.firing, key)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func (s *Service) getRule(ctx context.Context, id int64) (*Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, container_filter, metric, operator, threshold,
		       duration_seconds, channel_ids, enabled,
		       severity, cooldown_seconds, muted_until,
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
	var enabled int
	var severity sql.NullString
	var mutedUntil sql.NullTime
	var firingSince, lastT, lastR sql.NullTime
	if err := r.Scan(
		&rule.ID, &rule.Name, &rule.ContainerFilter, &rule.Metric, &rule.Operator,
		&rule.Threshold, &rule.DurationSeconds, &ids, &enabled,
		&severity, &rule.CooldownSeconds, &mutedUntil,
		&firingSince, &lastT, &lastR, &rule.CreatedAt, &rule.UpdatedAt,
	); err != nil {
		return nil, err
	}
	rule.Enabled = enabled == 1
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

func validateInput(in RuleInput) error {
	if in.Name == "" {
		return errors.New("name required")
	}
	if in.Metric != "cpu_percent" && in.Metric != "mem_percent" {
		return ErrInvalidMetric
	}
	if in.Operator != "gt" && in.Operator != "lt" {
		return ErrInvalidOperator
	}
	if in.Threshold == 0 {
		return ErrInvalidThreshold
	}
	if in.DurationSeconds < 0 {
		return errors.New("duration_seconds must be >= 0")
	}
	if in.ContainerFilter == "" {
		return errors.New("container_filter required (use '*' for all)")
	}
	return nil
}

func stateKey(ruleID int64, container string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s", ruleID, container)
	return b.String()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
