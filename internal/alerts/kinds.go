package alerts

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
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

// HostBound reports whether rules of this kind watch one host's data.
// Backup and certificate rules are global.
func HostBound(kind string) bool {
	return kind != KindBackupFailed && kind != KindBackupMissed && kind != KindCertExpiry
}

func containerKind(kind string) bool {
	switch kind {
	case KindContainerStopped, KindRestartLoop, KindUnhealthy, KindOOM:
		return true
	}
	return false
}

// Agents are not trusted to send what dockerd would: names end up in
// Slack/Teams markup and terminal output, so anything outside Docker's
// name charset is dropped.
var (
	dockerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	exitCode   = regexp.MustCompile(`^-?[0-9]{1,10}$`)
	mountPath  = regexp.MustCompile(`^/[A-Za-z0-9 ._@+/-]{0,254}$`)
	roleName   = regexp.MustCompile(`^[a-z_-]{1,32}$`)
)

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
	// eventsPerSecond caps events taken from one host; a crash-looping
	// fleet stays far below it, a misbehaving agent can't flood the DB.
	eventsPerSecond = 50
	maxEventField   = 255
	// stateTTL drops cooldown and stop bookkeeping for targets that went
	// quiet; it exceeds the longest cooldown the editor offers.
	stateTTL = 7 * 24 * time.Hour

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
		// Checked under evMu, which Stop holds while cancelling watchers, so
		// none starts after Stop and Stop waits for all of them.
		s.evMu.Lock()
		if s.stopped.Load() {
			s.evMu.Unlock()
			cancel()
			return
		}
		s.watching[id] = cancel
		s.wg.Add(1)
		s.evMu.Unlock()
		go func() {
			defer s.wg.Done()
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

// allowEvent applies the per-host rate limit; it logs once per second
// window in which events were dropped.
func (s *Service) allowEvent(hostID string, now time.Time) bool {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	b := s.rate[hostID]
	if now.Sub(b.start) >= time.Second {
		b = eventBucket{start: now}
	}
	b.n++
	s.rate[hostID] = b
	if b.n == eventsPerSecond+1 {
		slog.Warn("alerts: dropping docker events, host exceeds rate limit", "host", hostID, "per_second", eventsPerSecond)
	}
	return b.n <= eventsPerSecond
}

func truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

func (s *Service) handleEvent(ctx context.Context, ev ContainerEvent) {
	if !dockerName.MatchString(ev.Container) || !s.allowEvent(ev.HostID, ev.Time) {
		return
	}
	ev.Container = truncate(ev.Container, maxEventField)
	ev.Stack = truncate(ev.Stack, maxEventField)
	if !dockerName.MatchString(ev.Stack) {
		ev.Stack = ""
	}
	if !exitCode.MatchString(ev.ExitCode) {
		ev.ExitCode = ""
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

	tgt := target{hostID: ev.HostID, name: targetName(ev.HostID, ev.HostName, ev.Container)}
	where := ev.Container
	if ev.HostName != "" {
		where += " on " + ev.HostName
	}
	for _, r := range s.ruleSnapshot() {
		if !r.Enabled || !containerKind(r.Kind) || !matchTarget(r.ContainerFilter, ev.Container) ||
			!s.inScope(&r, ev.HostID, ev.Stack) {
			continue
		}
		key := keyOf(r.ID, tgt)
		firing := s.isFiring(key)
		switch r.Kind {
		case KindContainerStopped:
			switch {
			case ev.Action == "die" && ev.ExitCode != "0" && !userStop && !firing:
				s.fireRule(ctx, &r, tgt, i18n.Msg("alert.container_stopped", where, ev.ExitCode), 0, ev.Time)
			case ev.Action == "start" && firing:
				s.resolveRule(ctx, &r, tgt, i18n.Msg("alert.container_running", where), 0, ev.Time)
			case ev.Action == "destroy" && firing:
				s.resolveRule(ctx, &r, tgt, i18n.Msg("alert.container_removed", where), 0, ev.Time)
			}
		case KindRestartLoop:
			window := time.Duration(r.DurationSeconds) * time.Second
			switch {
			case ev.Action == "die" && !userStop:
				n := s.recordDie(key, ev.Time, window, max(int(r.Threshold), 1))
				if float64(n) >= r.Threshold && !firing {
					s.fireRule(ctx, &r, tgt, i18n.Msg("alert.container_restarts", where, n, window.String()), float64(n), ev.Time)
				}
			case ev.Action == "destroy":
				s.forgetDies(key)
				if firing {
					s.resolveRule(ctx, &r, tgt, i18n.Msg("alert.container_removed", where), 0, ev.Time)
				}
			}
		case KindUnhealthy:
			switch {
			case ev.Action == "health_status: unhealthy" && !firing:
				s.fireRule(ctx, &r, tgt, i18n.Msg("alert.container_unhealthy", where), 0, ev.Time)
			case (ev.Action == "health_status: healthy" || ev.Action == "destroy") && firing:
				s.resolveRule(ctx, &r, tgt, i18n.Msg("alert.container_healthy", where), 0, ev.Time)
			}
		case KindOOM:
			if ev.Action == "oom" {
				s.fireRule(ctx, &r, tgt, i18n.Msg("alert.container_oom", where), 0, ev.Time)
			}
		}
	}
}

// recordDie appends t to key's die history, drops entries older than
// window and returns how many remain, at most keep: only reaching the
// threshold matters. A container beyond the rule's target cap on its
// host is not tracked and counts zero.
func (s *Service) recordDie(key tkey, t time.Time, window time.Duration, keep int) int {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	prev, _ := s.dies.get(key)
	kept := append(pruneBefore(prev, t.Add(-window)), t)
	if len(kept) > keep {
		kept = append(kept[:0], kept[len(kept)-keep:]...)
	}
	if !s.dies.set(key, kept) {
		return 0
	}
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

func (s *Service) forgetDies(key tkey) {
	s.evMu.Lock()
	s.dies.del(key)
	s.evMu.Unlock()
}

// expireRestartLoops drops die history that left the window and
// resolves restart-loop alerts with no restart left inside it.
func (s *Service) expireRestartLoops(ctx context.Context, r *Rule, now time.Time) {
	window := time.Duration(r.DurationSeconds) * time.Second
	s.evMu.Lock()
	for _, key := range s.dies.keysOf(r.ID) {
		ts, _ := s.dies.get(key)
		if left := pruneBefore(ts, now.Add(-window)); len(left) == 0 {
			s.dies.del(key)
		} else {
			s.dies.set(key, left)
		}
	}
	s.evMu.Unlock()

	s.stateMu.Lock()
	active := s.firing.keysOf(r.ID)
	s.stateMu.Unlock()
	for _, key := range active {
		s.evMu.Lock()
		_, recent := s.dies.get(key)
		s.evMu.Unlock()
		if !recent {
			s.resolveRule(ctx, r, target{hostID: key.host, name: key.target},
				i18n.Msg("alert.container_no_restart", key.target, window.String()), 0, now)
		}
	}
}

// pruneState forgets stop requests past their grace period, cooldown and
// repeat bookkeeping of targets that are quiet again, and state of rules
// that no longer exist.
func (s *Service) pruneState(now time.Time) {
	cooldowns := map[int64]time.Duration{}
	for _, r := range s.ruleSnapshot() {
		cooldowns[r.ID] = r.repeatWindow()
	}
	s.evMu.Lock()
	for k, t := range s.stopping {
		if now.Sub(t) > stopGrace {
			delete(s.stopping, k)
		}
	}
	for k, b := range s.rate {
		if now.Sub(b.start) > time.Minute {
			delete(s.rate, k)
		}
	}
	for rh := range s.dies {
		if _, ok := cooldowns[rh.rule]; !ok {
			delete(s.dies, rh)
		}
	}
	s.evMu.Unlock()

	// quiet reports whether a timestamp of rule is past the rule's
	// cooldown, or the rule is gone.
	quiet := func(rule int64, t time.Time) bool {
		window, ok := cooldowns[rule]
		return !ok || now.Sub(t) > min(window, stateTTL)
	}
	s.stateMu.Lock()
	for rh, inner := range s.lastNotified {
		for name, t := range inner {
			k := tkey{rh.rule, rh.host, name}
			if _, firing := s.firing.get(k); !firing && quiet(rh.rule, t) {
				s.lastNotified.del(k)
			}
		}
	}
	for rh, inner := range s.lastRows {
		for name, row := range inner {
			if quiet(rh.rule, row.at) {
				s.lastRows.del(tkey{rh.rule, rh.host, name})
			}
		}
	}
	for rh := range s.firing {
		if _, ok := cooldowns[rh.rule]; !ok {
			delete(s.firing, rh)
		}
	}
	clear(s.capWarned)
	s.stateMu.Unlock()
}

// historyRetention is how long alert_history keeps entries. The newest
// fire of an alert that is still active is kept whatever its age, so a
// long-running alert survives the prune and a restart.
const (
	historyRetention = 30 * 24 * time.Hour
	historyPruneTick = time.Hour
	historyPruneMax  = 5000
	// historyPruneBatches bounds one pass; a backlog continues on the
	// next evaluation instead of holding the write lock for long.
	historyPruneBatches = 20
)

// pruneHistory starts a background pass that deletes expired history in
// batches, at most once per historyPruneTick.
func (s *Service) pruneHistory(ctx context.Context, now time.Time) {
	if (now.Sub(s.prunedAt) < historyPruneTick && !s.pruneMore.Load()) || !s.pruneBusy.CompareAndSwap(false, true) {
		return
	}
	s.prunedAt = now
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.pruneBusy.Store(false)
		s.pruneMore.Store(s.pruneHistoryNow(ctx, now))
	}()
}

// pruneHistoryNow runs one pass and reports whether expired entries are
// left. The keep check is correlated on the (rule, host, target) index,
// so each batch costs its own size, not a scan of the table.
func (s *Service) pruneHistoryNow(ctx context.Context, now time.Time) bool {
	cutoff := dbTime(now.Add(-historyRetention))
	total := int64(0)
	defer func() {
		if total > 0 {
			slog.Info("alerts: pruned history", "entries", total, "older_than", historyRetention)
		}
	}()
	for i := 0; i < historyPruneBatches; i++ {
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM alert_history WHERE id IN (
				SELECT h.id FROM alert_history h
				 WHERE h.occurred_at < ?
				   AND NOT (h.status = 'fired'
				        AND h.rule_id IN (SELECT id FROM alert_rules WHERE kind NOT IN (?, ?))
				        AND h.id = (SELECT MAX(x.id) FROM alert_history x
				                     WHERE x.rule_id = h.rule_id AND x.host_id = h.host_id
				                       AND x.container_name = h.container_name))
				 LIMIT ?)`, cutoff, KindOOM, KindBackupFailed, historyPruneMax)
		if err != nil {
			slog.Warn("alerts: prune history", "err", err)
			return false
		}
		n, _ := res.RowsAffected()
		total += n
		if n < historyPruneMax {
			return false
		}
	}
	return true
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
		at := target{hostID: a.id, name: a.name}
		key := keyOf(r.ID, at)
		firing := s.isFiring(key)
		offline := a.status == "offline" && a.seen.Valid && now.Sub(a.seen.Time) >= grace
		switch {
		case offline && !firing:
			s.fireRule(ctx, r, at, i18n.Msg("alert.agent_offline",
				a.name, a.seen.Time.Local().Format("2006-01-02 15:04")), 0, now)
		case !offline && firing && a.status != "offline":
			s.resolveRule(ctx, r, at, i18n.Msg("alert.agent_status", a.name, a.status), 0, now)
		}
	}
	return nil
}

func (s *Service) evalDisk(ctx context.Context, r *Rule, hosts []HostMounts, now time.Time) {
	for _, hm := range hosts {
		if !s.inScope(r, hm.HostID, "") {
			continue
		}
		seen := map[string]bool{}
		for _, m := range validMounts(hm.Mounts) {
			tgt := targetName(hm.HostID, hm.HostName, m.MountPoint)
			seen[tgt] = true
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
			key := keyOf(r.ID, target{hostID: hm.HostID, name: tgt})
			firing := s.isFiring(key)
			switch {
			case m.Percent >= r.Threshold && !firing:
				s.fireRule(ctx, r, target{hostID: hm.HostID, name: tgt}, i18n.Msg("alert.disk_full",
					m.MountPoint, hm.HostName, m.Percent, strings.Join(m.Roles, ", ")), m.Percent, now)
			case firing && m.Percent < r.Threshold-diskHysteresis:
				s.resolveRule(ctx, r, target{hostID: hm.HostID, name: tgt}, i18n.Msg("alert.disk_back",
					m.MountPoint, hm.HostName, m.Percent), m.Percent, now)
			}
		}
		s.resolveGoneMounts(ctx, r, hm, seen, now)
	}
}

// resolveGoneMounts resolves this host's disk alerts whose mount is no
// longer reported, e.g. after an unmount; nothing would clear them otherwise.
func (s *Service) resolveGoneMounts(ctx context.Context, r *Rule, hm HostMounts, seen map[string]bool, now time.Time) {
	var gone []string
	s.stateMu.Lock()
	for _, key := range s.firing.keysOf(r.ID) {
		if key.host == hm.HostID && !seen[key.target] {
			gone = append(gone, key.target)
		}
	}
	s.stateMu.Unlock()
	hostPrefix := targetName(hm.HostID, hm.HostName, "")
	for _, tgt := range gone {
		s.resolveRule(ctx, r, target{hostID: hm.HostID, name: tgt}, i18n.Msg("alert.disk_gone",
			strings.TrimPrefix(tgt, hostPrefix), hm.HostName), 0, now)
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
		msg := i18n.Msg("alert.backup_failed", f.jobName)
		if f.errMsg != "" {
			msg = i18n.Msg("alert.backup_failed_detail", f.jobName, f.errMsg)
		}
		s.fireRule(ctx, r, target{name: f.jobName}, msg, 0, now)
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
		         WHERE r.job_id = j.id AND r.status IN ('success', 'success_with_warnings') AND r.finished_at IS NOT NULL
		         ORDER BY r.id DESC LIMIT 1)
		  FROM backup_jobs j
		 WHERE j.deleted_at IS NULL`)
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
		key := keyOf(r.ID, target{name: j.name})
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
			s.fireRule(ctx, r, target{name: j.name}, i18n.Msg("alert.backup_missed",
				j.name, since.Local().Format("2006-01-02 15:04"), interval.String()), 0, now)
		case !overdue && firing:
			s.resolveRule(ctx, r, target{name: j.name}, i18n.Msg("alert.backup_on_schedule", j.name), 0, now)
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
		key := keyOf(r.ID, target{name: c.Domain})
		firing := s.isFiring(key)
		days := c.NotAfter.Sub(now).Hours() / 24
		switch {
		case days < r.Threshold && !firing:
			s.fireRule(ctx, r, target{name: c.Domain}, i18n.Msg("alert.cert_expires",
				c.Domain, c.NotAfter.Local().Format("2006-01-02"), days), days, now)
		case days >= r.Threshold && firing:
			s.resolveRule(ctx, r, target{name: c.Domain}, i18n.Msg("alert.cert_renewed",
				c.Domain, c.NotAfter.Local().Format("2006-01-02")), days, now)
		}
	}
}

// maxMountsPerHost bounds the mounts one host's disk report adds.
const maxMountsPerHost = 64

// validMounts drops mounts whose path or roles an agent made up: both end
// up in channel markup and in history.
func validMounts(in []system.Mount) []system.Mount {
	out := make([]system.Mount, 0, min(len(in), maxMountsPerHost))
	for _, m := range in {
		if len(out) == maxMountsPerHost {
			break
		}
		if !mountPath.MatchString(m.MountPoint) {
			continue
		}
		roles := make([]string, 0, len(m.Roles))
		for _, role := range m.Roles {
			if roleName.MatchString(role) {
				roles = append(roles, role)
			}
		}
		m.Roles = roles
		out = append(out, m)
	}
	return out
}
