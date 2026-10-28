package alerts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/system"
	"github.com/robfig/cron/v3"
)

// Rule kinds. For the event kinds, container_filter is the target filter
// ("*" or an exact container name, mount point, backup job name or proxy
// domain), threshold and duration_seconds carry the kind's parameters.
const (
	KindMetric           = "metric"
	KindContainerStopped = "container_stopped"
	KindRestartLoop      = "restart_loop" // threshold restarts within duration_seconds
	KindUnhealthy        = "unhealthy"
	KindOOM              = "oom"
	KindHostOffline      = "host_offline" // offline longer than duration_seconds
	KindDiskUsage        = "disk_usage"   // threshold percent; "*" = Docker-relevant mounts
	KindBackupFailed     = "backup_failed"
	KindBackupMissed     = "backup_missed"
	KindCertExpiry       = "cert_expiry" // threshold days
)

// oneShot kinds report a single occurrence; there is no state to resolve.
func oneShot(kind string) bool { return kind == KindOOM || kind == KindBackupFailed }

func containerKind(kind string) bool {
	switch kind {
	case KindContainerStopped, KindRestartLoop, KindUnhealthy, KindOOM:
		return true
	}
	return false
}

// ContainerEvent is a Docker container event from any host.
type ContainerEvent struct {
	HostID    string
	HostName  string
	Container string
	Stack     string
	Action    string
	ExitCode  string
	Time      time.Time
}

// EventHost is a host whose Docker events can be watched.
type EventHost interface {
	ID() string
	Name() string
	ContainerEvents(ctx context.Context) (<-chan ContainerEvent, error)
}

// ErrEventsUnsupported is returned by EventHost for agents that predate
// the events stream.
var ErrEventsUnsupported = errors.New("host cannot stream docker events")

// HostMounts is the per-mount disk usage of one host.
type HostMounts struct {
	HostID   string
	HostName string
	Mounts   []system.Mount
}

// CertStatus is the certificate a proxy route currently serves.
type CertStatus struct {
	Domain   string
	NotAfter time.Time
}

// Sources feeds the event rules. Nil funcs disable the kinds needing them.
type Sources struct {
	EventHosts func(ctx context.Context) []EventHost
	Mounts     func(ctx context.Context) []HostMounts
	HostTags   func(hostID string) []string
	Certs      func(ctx context.Context) []CertStatus
}

const (
	// stopGrace: a die this soon after kill/stop was asked for, not a crash.
	stopGrace      = time.Minute
	diskHysteresis = 2.0
	certRefresh    = 6 * time.Hour
	watchInterval  = 30 * time.Second
)

// SetSources wires the event inputs. Safe to call after Start.
func (s *Service) SetSources(src Sources) {
	s.srcMu.Lock()
	s.sources = src
	s.srcMu.Unlock()
}

// SetNotices lets the engine tell admins about agents it can't watch.
func (s *Service) SetNotices(n *notices.Service) {
	s.srcMu.Lock()
	s.notices = n
	s.srcMu.Unlock()
}

func (s *Service) src() Sources {
	s.srcMu.Lock()
	defer s.srcMu.Unlock()
	return s.sources
}

func (s *Service) setRules(rules []Rule) {
	s.rulesMu.Lock()
	s.rules = rules
	s.rulesMu.Unlock()
}

func (s *Service) ruleSnapshot() []Rule {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	return s.rules
}

// targetName keeps local targets unprefixed, matching the names the
// metric rules have always written to history.
func targetName(hostID, hostName, name string) string {
	if hostID == "" || hostID == "local" {
		return name
	}
	return hostName + "/" + name
}

func matchTarget(filter, name string) bool { return filter == "*" || filter == name }

func (s *Service) inScope(r *Rule, hostID, stack string) bool {
	if r.ScopeHost != "" && r.ScopeHost != hostID {
		return false
	}
	if r.ScopeStack != "" && r.ScopeStack != stack {
		return false
	}
	if r.ScopeTag != "" {
		tags := s.src().HostTags
		if tags == nil {
			return false
		}
		for _, t := range tags(hostID) {
			if t == r.ScopeTag {
				return true
			}
		}
		return false
	}
	return true
}

// -----------------------------------------------------------------------------
// Docker events
// -----------------------------------------------------------------------------

func (s *Service) watchLoop(ctx context.Context) {
	defer s.wg.Done()
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		s.syncWatchers(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}

// syncWatchers starts a watcher for every reachable host without one.
// Watchers end on their own when the host's stream closes and are
// restarted on a later pass.
func (s *Service) syncWatchers(ctx context.Context) {
	hosts := s.src().EventHosts
	if hosts == nil {
		return
	}
	var unsupported []string
	for _, h := range hosts(ctx) {
		id := h.ID()
		s.evMu.Lock()
		_, running := s.watching[id]
		s.evMu.Unlock()
		if running {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		ch, err := h.ContainerEvents(wctx)
		if err != nil {
			cancel()
			if errors.Is(err, ErrEventsUnsupported) {
				unsupported = append(unsupported, h.Name())
			} else {
				slog.Debug("alerts: watch docker events", "host", h.Name(), "err", err)
			}
			continue
		}
		s.evMu.Lock()
		s.watching[id] = cancel
		s.evMu.Unlock()
		go func() {
			defer func() {
				cancel()
				s.evMu.Lock()
				delete(s.watching, id)
				s.evMu.Unlock()
			}()
			for ev := range ch {
				s.handleEvent(ctx, ev)
			}
		}()
	}
	s.reportUnsupported(ctx, unsupported)
}

const unsupportedNoticeKey = "alerts.agents-without-events"

func (s *Service) reportUnsupported(ctx context.Context, names []string) {
	sort.Strings(names)
	joined := strings.Join(names, ", ")
	if joined == s.unsupported {
		return
	}
	s.unsupported = joined
	s.srcMu.Lock()
	n := s.notices
	s.srcMu.Unlock()
	if n == nil {
		return
	}
	if err := n.Resolve(ctx, unsupportedNoticeKey); err != nil {
		slog.Warn("alerts: resolve notice", "err", err)
	}
	if joined == "" {
		return
	}
	if err := n.Raise(ctx, notices.RaiseInput{
		Key:      unsupportedNoticeKey,
		Audience: notices.Admins(),
		Severity: notices.SevInfo,
		Title:    "Some agents are too old for container event alerts",
		Body: "Update the agent on " + joined + ". Until then, alerts for restart loops, OOM kills, " +
			"unhealthy containers and unexpected stops don't cover these hosts.",
		Link: "/agents",
	}); err != nil {
		slog.Warn("alerts: raise notice", "err", err)
	}
}

func (s *Service) handleEvent(ctx context.Context, ev ContainerEvent) {
	if ev.Container == "" {
		return
	}
	ckey := ev.HostID + "/" + ev.Container
	s.evMu.Lock()
	switch ev.Action {
	case "kill", "stop":
		s.stopping[ckey] = ev.Time
	}
	stoppedAt, asked := s.stopping[ckey]
	userStop := asked && ev.Time.Sub(stoppedAt) < stopGrace
	if ev.Action == "destroy" {
		delete(s.stopping, ckey)
	}
	s.evMu.Unlock()

	tgt := targetName(ev.HostID, ev.HostName, ev.Container)
	where := ev.Container
	if ev.HostName != "" {
		where += " on " + ev.HostName
	}
	for _, r := range s.ruleSnapshot() {
		if !r.Enabled || !containerKind(r.Kind) || !matchTarget(r.ContainerFilter, ev.Container) ||
			!s.inScope(&r, ev.HostID, ev.Stack) {
			continue
		}
		key := stateKey(r.ID, tgt)
		firing := s.isFiring(key)
		switch r.Kind {
		case KindContainerStopped:
			switch {
			case ev.Action == "die" && ev.ExitCode != "0" && !userStop && !firing:
				s.fireRule(ctx, &r, tgt, fmt.Sprintf("Container %s stopped unexpectedly (exit code %s)", where, ev.ExitCode), 0, ev.Time)
			case ev.Action == "start" && firing:
				s.resolveRule(ctx, &r, tgt, fmt.Sprintf("Container %s is running again", where), 0, ev.Time)
			case ev.Action == "destroy" && firing:
				s.resolveRule(ctx, &r, tgt, fmt.Sprintf("Container %s was removed", where), 0, ev.Time)
			}
		case KindRestartLoop:
			window := time.Duration(r.DurationSeconds) * time.Second
			switch {
			case ev.Action == "die" && !userStop:
				n := s.recordDie(key, ev.Time, window)
				if float64(n) >= r.Threshold && !firing {
					s.fireRule(ctx, &r, tgt, fmt.Sprintf("Container %s restarted %d times in %s", where, n, window), float64(n), ev.Time)
				}
			case ev.Action == "destroy" && firing:
				s.resolveRule(ctx, &r, tgt, fmt.Sprintf("Container %s was removed", where), 0, ev.Time)
			}
		case KindUnhealthy:
			switch {
			case ev.Action == "health_status: unhealthy" && !firing:
				s.fireRule(ctx, &r, tgt, fmt.Sprintf("Container %s is unhealthy", where), 0, ev.Time)
			case (ev.Action == "health_status: healthy" || ev.Action == "destroy") && firing:
				s.resolveRule(ctx, &r, tgt, fmt.Sprintf("Container %s is healthy again", where), 0, ev.Time)
			}
		case KindOOM:
			if ev.Action == "oom" {
				s.fireRule(ctx, &r, tgt, fmt.Sprintf("Container %s was killed for running out of memory", where), 0, ev.Time)
			}
		}
	}
}

// recordDie appends t to key's die history, drops entries older than
// window and returns how many remain.
func (s *Service) recordDie(key string, t time.Time, window time.Duration) int {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	kept := pruneBefore(s.dies[key], t.Add(-window))
	kept = append(kept, t)
	s.dies[key] = kept
	return len(kept)
}

func pruneBefore(ts []time.Time, cutoff time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

// expireRestartLoops resolves restart-loop alerts with no restart left
// inside the window.
func (s *Service) expireRestartLoops(ctx context.Context, r *Rule, now time.Time) {
	window := time.Duration(r.DurationSeconds) * time.Second
	prefix := fmt.Sprintf("%d|", r.ID)
	var quiet []string
	s.stateMu.Lock()
	for key := range s.firing {
		if strings.HasPrefix(key, prefix) {
			quiet = append(quiet, key)
		}
	}
	s.stateMu.Unlock()
	for _, key := range quiet {
		s.evMu.Lock()
		left := pruneBefore(s.dies[key], now.Add(-window))
		if len(left) == 0 {
			delete(s.dies, key)
		} else {
			s.dies[key] = left
		}
		s.evMu.Unlock()
		if len(left) == 0 {
			tgt := strings.TrimPrefix(key, prefix)
			s.resolveRule(ctx, r, tgt, fmt.Sprintf("Container %s has not restarted in %s", tgt, window), 0, now)
		}
	}
}

// -----------------------------------------------------------------------------
// Polled kinds
// -----------------------------------------------------------------------------

func (s *Service) evalHostOffline(ctx context.Context, r *Rule, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, status, last_seen_at FROM agents`)
	if err != nil {
		return err
	}
	type agentRow struct {
		id, name, status string
		seen             sql.NullTime
	}
	var list []agentRow
	for rows.Next() {
		var a agentRow
		if err := rows.Scan(&a.id, &a.name, &a.status, &a.seen); err != nil {
			rows.Close()
			return err
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	grace := time.Duration(r.DurationSeconds) * time.Second
	for _, a := range list {
		if !matchTarget(r.ContainerFilter, a.name) || !s.inScope(r, a.id, "") {
			continue
		}
		key := stateKey(r.ID, a.name)
		firing := s.isFiring(key)
		offline := a.status == "offline" && a.seen.Valid && now.Sub(a.seen.Time) >= grace
		switch {
		case offline && !firing:
			s.fireRule(ctx, r, a.name, fmt.Sprintf("Agent %s has been offline since %s",
				a.name, a.seen.Time.Local().Format("2006-01-02 15:04")), 0, now)
		case !offline && firing && a.status != "offline":
			s.resolveRule(ctx, r, a.name, fmt.Sprintf("Agent %s is %s", a.name, a.status), 0, now)
		}
	}
	return nil
}

func (s *Service) evalDisk(ctx context.Context, r *Rule, hosts []HostMounts, now time.Time) {
	for _, hm := range hosts {
		if !s.inScope(r, hm.HostID, "") {
			continue
		}
		for _, m := range hm.Mounts {
			if m.Total == 0 {
				continue
			}
			if r.ContainerFilter == "*" {
				if !m.DockerRelevant() {
					continue
				}
			} else if r.ContainerFilter != m.MountPoint {
				continue
			}
			tgt := targetName(hm.HostID, hm.HostName, m.MountPoint)
			key := stateKey(r.ID, tgt)
			firing := s.isFiring(key)
			switch {
			case m.Percent >= r.Threshold && !firing:
				s.fireRule(ctx, r, tgt, fmt.Sprintf("Disk %s on %s is %.0f%% full (%s)",
					m.MountPoint, hm.HostName, m.Percent, strings.Join(m.Roles, ", ")), m.Percent, now)
			case firing && m.Percent < r.Threshold-diskHysteresis:
				s.resolveRule(ctx, r, tgt, fmt.Sprintf("Disk %s on %s is back to %.0f%%",
					m.MountPoint, hm.HostName, m.Percent), m.Percent, now)
			}
		}
	}
}

type failedRun struct {
	id      int64
	jobName string
	errMsg  string
}

// newFailedRuns returns runs that failed since the last call. The first
// call only records the high-water mark so a restart doesn't re-announce
// old failures.
func (s *Service) newFailedRuns(ctx context.Context) ([]failedRun, error) {
	if s.lastRunID < 0 {
		var maxID sql.NullInt64
		if err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM backup_runs`).Scan(&maxID); err != nil {
			return nil, err
		}
		s.lastRunID = maxID.Int64
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, job_name, status, COALESCE(error, '') FROM backup_runs
		 WHERE id > ? AND status != 'running' ORDER BY id`, s.lastRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []failedRun
	for rows.Next() {
		var f failedRun
		var status string
		if err := rows.Scan(&f.id, &f.jobName, &status, &f.errMsg); err != nil {
			return out, err
		}
		s.lastRunID = f.id
		if status == "failed" {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}

func (s *Service) evalBackupFailed(ctx context.Context, r *Rule, runs []failedRun, now time.Time) {
	for _, f := range runs {
		if !matchTarget(r.ContainerFilter, f.jobName) {
			continue
		}
		msg := fmt.Sprintf("Backup job %s failed", f.jobName)
		if f.errMsg != "" {
			msg += ": " + f.errMsg
		}
		s.fireRule(ctx, r, f.jobName, msg, 0, now)
	}
}

// backupOverdue reports whether a job with the given cron schedule has
// gone longer than one interval plus grace without a successful run.
func backupOverdue(schedule string, since, now time.Time) (bool, time.Duration) {
	sched, err := cron.ParseStandard(schedule)
	if err != nil {
		return false, 0
	}
	n1 := sched.Next(now)
	interval := sched.Next(n1).Sub(n1)
	grace := interval / 4
	if grace < 15*time.Minute {
		grace = 15 * time.Minute
	}
	return now.Sub(since) > interval+grace, interval
}

func (s *Service) evalBackupMissed(ctx context.Context, r *Rule, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.name, j.schedule, j.enabled, j.created_at,
		       (SELECT r.finished_at FROM backup_runs r
		         WHERE r.job_id = j.id AND r.status = 'success' AND r.finished_at IS NOT NULL
		         ORDER BY r.id DESC LIMIT 1)
		  FROM backup_jobs j`)
	if err != nil {
		return err
	}
	type jobRow struct {
		name, schedule string
		enabled        bool
		created        time.Time
		lastOK         sql.NullTime
	}
	var jobs []jobRow
	for rows.Next() {
		var j jobRow
		var enabled int
		var lastOK sql.NullString
		if err := rows.Scan(&j.name, &j.schedule, &enabled, &j.created, &lastOK); err != nil {
			rows.Close()
			return err
		}
		j.enabled = enabled == 1
		if lastOK.Valid {
			if t, ok := parseDBTime(lastOK.String); ok {
				j.lastOK = sql.NullTime{Time: t, Valid: true}
			}
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, j := range jobs {
		if !matchTarget(r.ContainerFilter, j.name) {
			continue
		}
		key := stateKey(r.ID, j.name)
		firing := s.isFiring(key)
		since := j.created
		if j.lastOK.Valid {
			since = j.lastOK.Time
		}
		overdue, interval := false, time.Duration(0)
		if j.enabled && strings.TrimSpace(j.schedule) != "" {
			overdue, interval = backupOverdue(j.schedule, since, now)
		}
		switch {
		case overdue && !firing:
			s.fireRule(ctx, r, j.name, fmt.Sprintf("Backup job %s has had no successful run since %s (scheduled every %s)",
				j.name, since.Local().Format("2006-01-02 15:04"), interval), 0, now)
		case !overdue && firing:
			s.resolveRule(ctx, r, j.name, fmt.Sprintf("Backup job %s is back on schedule", j.name), 0, now)
		}
	}
	return nil
}

// parseDBTime reads the timestamp layouts found in DATETIME columns:
// SQLite's CURRENT_TIMESTAMP and Go's time.Time String form.
func parseDBTime(v string) (time.Time, bool) {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// refreshCerts reloads the proxy certificates in the background; probing
// every route over TLS is too slow for the evaluation loop.
func (s *Service) refreshCerts(ctx context.Context, now time.Time) {
	certs := s.src().Certs
	if certs == nil || now.Sub(s.certsAt) < certRefresh || !s.certsBusy.CompareAndSwap(false, true) {
		return
	}
	s.certsAt = now
	go func() {
		defer s.certsBusy.Store(false)
		list := certs(ctx)
		s.certs.Store(&list)
	}()
}

func (s *Service) evalCerts(ctx context.Context, r *Rule, now time.Time) {
	p := s.certs.Load()
	if p == nil {
		return
	}
	for _, c := range *p {
		if !matchTarget(r.ContainerFilter, c.Domain) {
			continue
		}
		key := stateKey(r.ID, c.Domain)
		firing := s.isFiring(key)
		days := c.NotAfter.Sub(now).Hours() / 24
		switch {
		case days < r.Threshold && !firing:
			s.fireRule(ctx, r, c.Domain, fmt.Sprintf("TLS certificate for %s expires %s (%.0f days)",
				c.Domain, c.NotAfter.Local().Format("2006-01-02"), days), days, now)
		case days >= r.Threshold && firing:
			s.resolveRule(ctx, r, c.Domain, fmt.Sprintf("TLS certificate for %s renewed, valid until %s",
				c.Domain, c.NotAfter.Local().Format("2006-01-02")), days, now)
		}
	}
}
