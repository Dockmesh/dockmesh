package proxy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// ErrApplyRejected is what a user sees when Caddy refuses a change: the
// change was rolled back, and Caddy's own text (which can quote other
// sites) stays in the server log.
var ErrApplyRejected = errors.New("Caddy rejected the configuration. Nothing was changed.") //nolint:staticcheck // shown to users as is

// ErrConfigRejected stands in for Caddy's own error when a new container
// refuses the stored config: that error can quote other sites'
// configuration, which proxy.view callers only see masked.
var ErrConfigRejected = errors.New("caddy rejected the configuration; the server log has the details")

// loadRejectedError is Caddy refusing a /load; it keeps running its
// previous config then.
type loadRejectedError struct {
	status int
	body   string
}

func (e *loadRejectedError) Error() string {
	return fmt.Sprintf("caddy /load %d: %s", e.status, e.body)
}

func isRejected(err error) bool {
	var r *loadRejectedError
	return errors.As(err, &r)
}

const (
	// noticeKeyOffline is raised while sites are left out of the Caddy
	// config because their stored state could not be loaded.
	noticeKeyOffline = "proxy.offline-sites"

	// backfillHash marks rows that existed before applied_hash did; they
	// were loaded as they are, so their current state counts as applied.
	backfillHash = "@backfill"

	loadTimeout = 10 * time.Second
	// txLoadTimeout bounds a /load made while a write transaction holds
	// the database lock: other writers wait at most busy_timeout (5 s)
	// for it, and a fallback can need a second load.
	txLoadTimeout = 2 * time.Second
)

// stateHash identifies a row's stored state: what decides its part of the
// Caddy config. It covers the stored options text, secrets included, so a
// changed secret counts as a change.
func stateHash(r *Route) string {
	b, _ := json.Marshal([]any{r.Host, r.Upstream, r.TLSMode, r.Enabled, r.stored})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// applied reports whether Caddy last loaded the row in its current state.
// A disabled row adds nothing to the config, so it never keeps a site
// offline.
func (r *Route) applied() bool {
	return !r.Enabled || r.appliedHash == stateHash(r)
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// resolveBackfill replaces the migration's marker. The version before
// applied_hash loaded every row it saved, so a marked row counts as loaded
// in its current state, unless Caddy doesn't run its site: then Caddy
// refused it after it was saved. Caddy is only asked when it answers as
// the managed container. Runs before any write, so a marker is never
// resolved against a state Caddy hasn't seen. Callers hold s.mu.
func (s *Service) resolveBackfill(ctx context.Context) error {
	routes, err := queryRoutes(ctx, s.db, ` WHERE applied_hash = ?`, backfillHash)
	if err != nil || len(routes) == 0 {
		return err
	}
	var running *runningServers
	if s.Enabled() && s.verifyManaged(ctx) == nil {
		if b, err := runningConfig(ctx); err == nil {
			running = parseRunning(b)
		}
	}
	for i := range routes {
		h := stateHash(&routes[i])
		if running != nil && !s.runsSite(running, routes[i]) {
			slog.Warn("proxy: caddy doesn't run the stored config of this site", "host", routes[i].Host)
			h = ""
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE proxy_routes SET applied_hash = ? WHERE id = ?`, h, routes[i].ID); err != nil {
			return err
		}
	}
	return nil
}

// runningServers is the part of a config read from /config/ that says
// what Caddy serves.
type runningServers struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Routes []any `json:"routes"`
			} `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
}

// parseRunning reads a config from /config/; nil when it can't be read.
func parseRunning(b []byte) *runningServers {
	var r runningServers
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// runsSite reports whether every route Dockmesh generates for r alone is
// in the server of the same name in running.
func (s *Service) runsSite(running *runningServers, r Route) bool {
	if !r.Enabled {
		return true
	}
	want, _ := s.configFor([]Route{r})
	for name, srv := range want.Apps.HTTP.Servers {
		have := running.Apps.HTTP.Servers[name].Routes
		for _, route := range srv.Routes {
			b, err := json.Marshal(route)
			if err != nil {
				return false
			}
			var w any
			if err := json.Unmarshal(b, &w); err != nil {
				return false
			}
			if !slices.ContainsFunc(have, func(h any) bool { return reflect.DeepEqual(w, h) }) {
				return false
			}
		}
	}
	return true
}

// markApplied records the rows Caddy now runs.
func markApplied(ctx context.Context, q querier, routes []Route) error {
	for i := range routes {
		h := stateHash(&routes[i])
		if routes[i].appliedHash == h {
			continue
		}
		if _, err := q.ExecContext(ctx, `UPDATE proxy_routes SET applied_hash = ? WHERE id = ?`, h, routes[i].ID); err != nil {
			return err
		}
	}
	return nil
}

// configFor builds the Caddy config for routes, addressed to the managed
// container, and returns the hosts left out as blocked.
func (s *Service) configFor(routes []Route) (*caddyConfig, []string) {
	cfg, blocked := buildConfig(routes, s.ProxySettings())
	if id := s.adminID.Load(); id != nil {
		cfg.Admin.ID = *id
	}
	if s.legacyAdmin.Load() {
		cfg.Admin.Listen, cfg.Admin.Origins = legacyAdminAddr, legacyAdminOrigins
	}
	return cfg, blocked
}

// loadResult is what a successful load put into Caddy.
type loadResult struct {
	cfg *caddyConfig
	// included are the rows the loaded config was built from; offline the
	// enabled rows left out because Caddy refused them.
	included, offline []Route
	blocked           []string
}

// load pushes the config for routes. When Caddy refuses it and fallback
// allows, it pushes the config of only the rows Caddy last loaded as they
// are, plus keep (the row a change is about, 0 for none). Callers hold
// s.mu and have checked that the managed Caddy answers.
func (s *Service) load(ctx context.Context, routes []Route, keep int64, fallback func() bool, timeout time.Duration) (*loadResult, error) {
	cfg, blocked := s.configFor(routes)
	err := postLoad(ctx, cfg, timeout)
	if err == nil {
		s.lastLoaded, s.lastRejected = cfg, ""
		return &loadResult{cfg: cfg, included: routes, blocked: blocked}, nil
	}
	if !isRejected(err) {
		return nil, err
	}
	slog.Warn("proxy: caddy rejected the config", "err", err)
	s.lastRejected = configKey(cfg)
	var included, offline []Route
	for _, r := range routes {
		if r.ID == keep || r.applied() {
			included = append(included, r)
		} else {
			offline = append(offline, r)
		}
	}
	if len(offline) == 0 || !fallback() {
		return nil, err
	}
	cfg, blocked = s.configFor(included)
	if err := postLoad(ctx, cfg, timeout); err != nil {
		if isRejected(err) {
			slog.Warn("proxy: caddy rejected the config without the sites it hasn't loaded yet", "err", err)
		}
		return nil, err
	}
	s.lastLoaded = cfg
	slog.Warn("proxy: sites left out because caddy rejected their config", "hosts", hostsOf(offline))
	return &loadResult{cfg: cfg, included: included, offline: offline, blocked: blocked}, nil
}

func always() bool { return true }

// configKey identifies a config, so the watchdog doesn't push one Caddy
// already refused again every interval.
func configKey(cfg *caddyConfig) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// applyOutcome says how far a saved change got.
type applyOutcome int

const (
	applyLoaded applyOutcome = iota
	// applyOff: the proxy is off; the change is used once it runs.
	applyOff
	// applyPending: Caddy did not answer; the watchdog pushes the change
	// once it does.
	applyPending
)

// applyChange runs write in a transaction and loads the config built from
// the transaction's view into Caddy. It commits once Caddy accepted the
// config, or right away when the proxy is off or Caddy doesn't answer, and
// rolls back when Caddy refuses it, so the database never holds a change
// that keeps every later one from loading. write returns the row it
// touched (0 when none is left).
func (s *Service) applyChange(ctx context.Context, write func(tx *sql.Tx) (int64, error)) (int64, applyOutcome, error) {
	// A client that goes away must not cut the load and the commit apart.
	ctx = context.WithoutCancel(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.resolveBackfill(ctx); err != nil {
		return 0, 0, err
	}
	outcome := applyLoaded
	if !s.Enabled() {
		outcome = applyOff
	} else if err := s.verifyManaged(ctx); err != nil {
		slog.Warn("proxy: caddy not reachable, the change is applied once it answers", "err", err)
		outcome = applyPending
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := write(tx)
	if err != nil {
		return 0, 0, err
	}
	if outcome != applyLoaded {
		return id, outcome, tx.Commit()
	}
	routes, err := queryRoutes(ctx, tx, "")
	if err != nil {
		return 0, 0, err
	}
	res, err := s.load(ctx, routes, id, always, txLoadTimeout)
	switch {
	case isRejected(err):
		return 0, 0, ErrApplyRejected
	case err != nil:
		// Caddy may or may not have taken the config; the watchdog
		// compares and pushes the stored state.
		slog.Warn("proxy: loading the change into caddy failed, it is applied once caddy answers", "err", err)
		return id, applyPending, tx.Commit()
	}
	if err := markApplied(ctx, tx, res.included); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	s.updateNotices(ctx, res)
	return id, applyLoaded, nil
}

// verifyManaged checks that the admin API answering is the managed Caddy.
func (s *Service) verifyManaged(ctx context.Context) error {
	id := ""
	if p := s.adminID.Load(); p != nil {
		id = *p
	}
	return verifyAdmin(ctx, id)
}

// syncLocked pushes the stored routes. When Caddy refuses them and
// fallback allows, the sites it hasn't loaded in their current state are
// left out and named in a notice. Callers hold s.mu.
func (s *Service) syncLocked(ctx context.Context, fallback func() bool) error {
	if err := s.resolveBackfill(ctx); err != nil {
		return err
	}
	routes, err := queryRoutes(ctx, s.db, "")
	if err != nil {
		return err
	}
	if err := s.verifyManaged(ctx); err != nil {
		return err
	}
	res, err := s.load(ctx, routes, 0, fallback, loadTimeout)
	if err != nil {
		if isRejected(err) {
			s.setOfflineNotice(ctx, unappliedRoutes(routes))
		}
		return err
	}
	if err := markApplied(ctx, s.db, res.included); err != nil {
		return err
	}
	s.updateNotices(ctx, res)
	return nil
}

// fallbackIfEmpty allows the fallback only while Caddy serves no routes,
// i.e. runs the bootstrap config of a new container. A container resumed
// from its autosave keeps serving the last config it loaded instead.
func fallbackIfEmpty(ctx context.Context) func() bool {
	return func() bool {
		running, err := runningConfig(ctx)
		return err == nil && !servesRoutes(running)
	}
}

// servesRoutes reports whether a config read from /config/ has any HTTP
// route; one that can't be read counts as serving.
func servesRoutes(running []byte) bool {
	r := parseRunning(running)
	if r == nil {
		return true
	}
	for _, sv := range r.Apps.HTTP.Servers {
		if len(sv.Routes) > 0 {
			return true
		}
	}
	return false
}

func unappliedRoutes(routes []Route) []Route {
	var out []Route
	for _, r := range routes {
		if !r.applied() {
			out = append(out, r)
		}
	}
	return out
}

func hostsOf(routes []Route) []string {
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		out = append(out, r.Host)
	}
	return out
}

func (s *Service) updateNotices(ctx context.Context, res *loadResult) {
	s.setOfflineNotice(ctx, res.offline)
	s.setBlockedNotice(ctx, res.blocked)
}

// setOfflineNotice tells admins which sites are not served because Caddy
// refused their stored state; an empty list clears it. The link opens the
// site when only one is affected.
func (s *Service) setOfflineNotice(ctx context.Context, offline []Route) {
	if s.notices == nil {
		return
	}
	link := "/proxy"
	if len(offline) == 1 {
		link = "/proxy/" + offline[0].Host
	}
	err := s.notices.Set(ctx, len(offline) > 0, notices.RaiseInput{
		Key:      noticeKeyOffline,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.proxy_offline_sites.title"),
		Body:     i18n.Msg("notice.proxy_offline_sites.body", notices.Bullets(hostsOf(offline))),
		Link:     link,
	})
	if err != nil {
		slog.Warn("proxy: update offline-sites notice failed", "err", err)
	}
}

func (s *Service) setBlockedNotice(ctx context.Context, blocked []string) {
	if s.notices == nil {
		return
	}
	err := s.notices.Set(ctx, len(blocked) > 0, notices.RaiseInput{
		Key:      noticeKeyBlocked,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.proxy_blocked_routes.title"),
		Body:     i18n.Msg("notice.proxy_blocked_routes.body", strings.Join(blocked, ", ")),
		Link:     "/proxy",
	})
	if err != nil {
		slog.Warn("proxy: update blocked-upstream notice failed", "err", err)
	}
}
