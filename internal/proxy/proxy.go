// Package proxy manages a Caddy reverse-proxy container. Running Caddy
// as a managed container keeps its large dependency graph out of the
// Dockmesh binary.
//
// Caddy runs on the host network so routes can reach localhost ports;
// its admin API therefore listens on 127.0.0.1:2019 of the host. Caddy
// checks Host and Origin on every admin request, which stops browsers
// and DNS rebinding, but any process on the host and any container with
// `network_mode: host` can still reach the API. Caddy offers no other
// authentication for its local admin endpoint short of a unix socket.
package proxy

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

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// Route is a single reverse-proxy entry. Host is matched against the
// incoming request and traffic is forwarded to Upstream.
type Route struct {
	ID       int64  `json:"id"`
	Host     string `json:"host"`
	Upstream string `json:"upstream"`
	TLSMode  string `json:"tls_mode"` // auto | internal | none
	// Enabled gates whether this route is in the generated config;
	// disabled routes stay in the table.
	Enabled bool `json:"enabled"`
	// Options are returned without basic-auth hashes (see Public).
	Options   SiteOptions `json:"options"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	// Cert metadata — populated by Service.EnrichWithCertInfo() for
	// routes whose TLS is managed by Caddy. Empty for tls_mode=none.
	CertIssuer    string     `json:"cert_issuer,omitempty"`
	CertValidFrom *time.Time `json:"cert_valid_from,omitempty"`
	CertValidTo   *time.Time `json:"cert_valid_to,omitempty"`
	CertDaysLeft  *int       `json:"cert_days_left,omitempty"`

	// optionsErr is set when the stored options can't be read; such a
	// route is not served rather than served without its access rules.
	optionsErr error
}

// RouteSpec is the input for a new route. Nil Options means defaults.
type RouteSpec struct {
	Host     string
	Upstream string
	TLSMode  string
	Options  *SiteOptions
	Disabled bool
	// hashed marks options whose basic-auth users carry bcrypt hashes
	// instead of passwords (imported from a Caddyfile).
	hashed bool
}

// Status reports whether the proxy container is running and whether the
// admin API is reachable.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	AdminOK   bool   `json:"admin_ok"`
	Version   string `json:"version,omitempty"`
	Container string `json:"container,omitempty"`
}

var (
	ErrProxyNotConfigured = errors.New("proxy not enabled")
	ErrInvalidTLSMode     = errors.New("invalid tls mode")
	ErrDuplicateHost      = errors.New("host already has a route")
	ErrRouteNotFound      = errors.New("route not found")
	ErrWWWConflict        = errors.New("the www. name of this host is a site of its own")
)

// noticeKeyBlocked is raised while routes are left out of the Caddy
// config because their upstream is the admin API.
const noticeKeyBlocked = "proxy.blocked-upstreams"

type noticeSetter interface {
	Set(ctx context.Context, active bool, in notices.RaiseInput) error
}

type Service struct {
	db       *sql.DB
	docker   *docker.Client
	enabled  atomic.Bool
	notices  noticeSetter
	settings SettingsStore

	// image is the Caddy image to run; imageOverride is set when it
	// comes from DOCKMESH_PROXY_IMAGE. switchMu serialises version
	// switches.
	imageMu       sync.RWMutex
	image         string
	imageOverride bool
	switchMu      sync.Mutex
	// startProxy replaces startAndVerify in tests.
	startProxy func(ctx context.Context) error

	// lifecycle serialises EnableProxy/DisableProxy; mu guards config
	// pushes and metrics.
	lifecycle sync.Mutex
	mu        sync.Mutex
	metrics   *metricsState
	acme      *acmeBuffer

	// certMu guards the RouteCerts cache.
	certMu     sync.Mutex
	certAt     time.Time
	certRoutes []Route

	// containerState reports whether the managed container exists and runs;
	// nil means ask Docker.
	containerState func(ctx context.Context) (exists, running bool)
}

func NewService(db *sql.DB, dockerCli *docker.Client, enabled bool) *Service {
	s := &Service{db: db, docker: dockerCli, image: DefaultProxyImage}
	s.enabled.Store(enabled)
	return s
}

func (s *Service) Enabled() bool { return s.enabled.Load() }

// SetImage overrides the Caddy image (DOCKMESH_PROXY_IMAGE). Empty keeps
// the pinned default.
func (s *Service) SetImage(image string) {
	if image != "" {
		s.setImage(image)
		s.imageOverride = true
	}
}

// SetNotices lets the proxy tell admins about routes it refuses to push.
func (s *Service) SetNotices(n noticeSetter) { s.notices = n }

// SyncFromDB loads all routes and pushes a fresh config to Caddy. Called
// at startup and after every mutation.
func (s *Service) SyncFromDB(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.desiredConfig(ctx)
	if err != nil {
		return err
	}
	return pushConfig(ctx, cfg)
}

// desiredConfig builds the Caddy config for the stored routes and keeps
// the blocked-upstream notice in line with it. Callers hold s.mu.
func (s *Service) desiredConfig(ctx context.Context) (*caddyConfig, error) {
	routes, err := s.listRoutes(ctx)
	if err != nil {
		return nil, err
	}
	cfg, blocked := buildConfig(routes, s.ProxySettings())
	if s.notices != nil {
		err := s.notices.Set(ctx, len(blocked) > 0, notices.RaiseInput{
			Key:      noticeKeyBlocked,
			Audience: notices.Admins(),
			Severity: notices.SevWarning,
			Title:    "Some proxy routes are not served",
			Body: "These routes have a host or upstream Dockmesh no longer accepts (for example the proxy's admin API, " +
				"localhost, or a placeholder), so they are left out of the proxy: " + strings.Join(blocked, ", ") +
				". Edit each one to a domain name and a host:port upstream, or delete it.",
			Link: "/proxy",
		})
		if err != nil {
			slog.Warn("proxy: update blocked-upstream notice failed", "err", err)
		}
	}
	return cfg, nil
}

// ListRoutes returns all configured routes, without password hashes.
func (s *Service) ListRoutes(ctx context.Context) ([]Route, error) {
	routes, err := s.listRoutes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range routes {
		routes[i].Options = routes[i].Options.Public()
	}
	return routes, nil
}

// GetRoute returns one route without password hashes.
func (s *Service) GetRoute(ctx context.Context, id int64) (*Route, error) {
	r, err := s.getRoute(ctx, id)
	if err != nil {
		return nil, err
	}
	r.Options = r.Options.Public()
	return r, nil
}

// CreateRoute adds a new host → upstream mapping.
func (s *Service) CreateRoute(ctx context.Context, spec RouteSpec) (*Route, error) {
	if err := validateTLSMode(spec.TLSMode); err != nil {
		return nil, err
	}
	if err := checkRouteHost(spec.Host); err != nil {
		return nil, err
	}
	if err := checkUpstream(spec.Upstream); err != nil {
		return nil, err
	}
	var opts SiteOptions
	if spec.Options != nil {
		opts = *spec.Options
	}
	var prev *SiteOptions
	if spec.hashed {
		hashes := opts
		prev = &hashes
	}
	raw, err := s.prepareOptions(ctx, 0, spec.Host, spec.TLSMode, &opts, prev)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO proxy_routes (host, upstream, tls_mode, enabled, options) VALUES (?, ?, ?, ?, ?)`,
		spec.Host, spec.Upstream, spec.TLSMode, boolToInt(!spec.Disabled), raw)
	if err != nil {
		// SQLite unique constraint code is vendor-specific; the string
		// "UNIQUE constraint" is stable enough.
		return nil, ErrDuplicateHost
	}
	id, _ := res.LastInsertId()
	route := &Route{ID: id, Host: spec.Host, Upstream: spec.Upstream, TLSMode: spec.TLSMode, Enabled: !spec.Disabled, Options: opts.Public()}
	if err := s.SyncFromDB(ctx); err != nil {
		return route, err
	}
	return route, nil
}

// UpdateRoute replaces the upstream and TLS mode of an existing route,
// its enabled state unless enabled is nil and its options unless opts is
// nil. A disabled route stays in the table but is left out of the
// generated config, so traffic stops without losing config.
func (s *Service) UpdateRoute(ctx context.Context, id int64, upstream, tlsMode string, enabled *bool, opts *SiteOptions) error {
	if err := validateTLSMode(tlsMode); err != nil {
		return err
	}
	if err := checkUpstream(upstream); err != nil {
		return err
	}
	cur, err := s.getRoute(ctx, id)
	if err != nil {
		return err
	}
	next := cur.Options
	prev := &cur.Options
	if opts != nil {
		next = *opts
	} else if cur.optionsErr != nil {
		return fmt.Errorf("%w: stored options can't be read (%w); send new options", ErrInvalidOptions, cur.optionsErr)
	}
	raw, err := s.prepareOptions(ctx, id, cur.Host, tlsMode, &next, prev)
	if err != nil {
		return err
	}
	on := cur.Enabled
	if enabled != nil {
		on = *enabled
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE proxy_routes SET upstream = ?, tls_mode = ?, enabled = ?, options = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		upstream, tlsMode, boolToInt(on), raw, id)
	if err != nil {
		return err
	}
	return s.SyncFromDB(ctx)
}

// prepareOptions hashes new passwords, validates the options of the
// site with id (0 for a new one) and returns them encoded for storage.
func (s *Service) prepareOptions(ctx context.Context, id int64, host, tlsMode string, o, prev *SiteOptions) (string, error) {
	if err := o.applyPasswords(prev); err != nil {
		return "", err
	}
	o.normalize()
	if err := o.validate(host, tlsMode); err != nil {
		return "", err
	}
	if err := s.checkWWWConflict(ctx, id, host, o.Routing.WWWRedirect); err != nil {
		return "", err
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// checkWWWConflict refuses a www redirect onto a name that is a site of
// its own, and a site whose name another site redirects.
func (s *Service) checkWWWConflict(ctx context.Context, id int64, host string, www bool) error {
	routes, err := s.listRoutes(ctx)
	if err != nil {
		return err
	}
	h := strings.ToLower(host)
	for _, r := range routes {
		if r.ID == id {
			continue
		}
		other := strings.ToLower(r.Host)
		if www && other == "www."+h {
			return ErrWWWConflict
		}
		if r.Options.Routing.WWWRedirect && h == "www."+other {
			return fmt.Errorf("%w: %s redirects it", ErrWWWConflict, r.Host)
		}
	}
	return nil
}

// SitePreview is the Caddy config Dockmesh generates for one site.
type SitePreview struct {
	RouteID int64 `json:"route_id"`
	Enabled bool  `json:"enabled"`
	// Blocked is true when the stored route fails validation and is
	// left out of the proxy.
	Blocked bool         `json:"blocked"`
	Config  *caddyConfig `json:"config"`
}

// SiteConfig returns the config the site gets when enabled, with
// basic-auth hashes redacted and without the admin section. With mask
// it is built from Masked options.
func (s *Service) SiteConfig(ctx context.Context, id int64, mask bool) (*SitePreview, error) {
	routes, err := s.listRoutes(ctx)
	if err != nil {
		return nil, err
	}
	var site *Route
	for i := range routes {
		if routes[i].ID == id {
			site = &routes[i]
		}
	}
	if site == nil {
		return nil, ErrRouteNotFound
	}
	one := *site
	one.Enabled = true
	for _, o := range routes {
		if o.ID != id && o.Enabled && strings.EqualFold(o.Host, "www."+site.Host) {
			one.Options.Routing.WWWRedirect = false
		}
	}
	if mask {
		one.Options = one.Options.Masked()
	}
	cfg, blocked := buildConfigWith([]Route{one}, s.ProxySettings(), true)
	cfg.Admin = nil
	return &SitePreview{RouteID: id, Enabled: site.Enabled, Blocked: len(blocked) > 0, Config: cfg}, nil
}

// SetRouteEnabled toggles only the enabled flag — used by the row-level
// disable/enable toggle in the UI so callers don't have to round-trip
// upstream + tls_mode unchanged.
func (s *Service) SetRouteEnabled(ctx context.Context, id int64, enabled bool) error {
	if enabled {
		r, err := s.getRoute(ctx, id)
		if err != nil && !errors.Is(err, ErrRouteNotFound) {
			return err
		}
		if err == nil {
			if err := checkRouteHost(r.Host); err != nil {
				return err
			}
			if err := checkUpstream(r.Upstream); err != nil {
				return err
			}
			if r.optionsErr != nil {
				return fmt.Errorf("%w: stored options can't be read; edit the site", ErrInvalidOptions)
			}
			if err := r.Options.validate(r.Host, r.TLSMode); err != nil {
				return err
			}
		}
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE proxy_routes SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		boolToInt(enabled), id)
	if err != nil {
		return err
	}
	return s.SyncFromDB(ctx)
}

// DeleteRoute removes a route.
func (s *Service) DeleteRoute(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM proxy_routes WHERE id = ?`, id); err != nil {
		return err
	}
	return s.SyncFromDB(ctx)
}

const routeColumns = `id, host, upstream, tls_mode, enabled, options, created_at, updated_at`

func (s *Service) listRoutes(ctx context.Context) ([]Route, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+routeColumns+` FROM proxy_routes ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Service) getRoute(ctx context.Context, id int64) (*Route, error) {
	r, err := scanRoute(s.db.QueryRowContext(ctx, `SELECT `+routeColumns+` FROM proxy_routes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRouteNotFound
	}
	return r, err
}

func scanRoute(row interface{ Scan(...any) error }) (*Route, error) {
	var r Route
	var enabledInt int
	var raw string
	if err := row.Scan(&r.ID, &r.Host, &r.Upstream, &r.TLSMode, &enabledInt, &raw, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Enabled = enabledInt != 0
	r.Options, r.optionsErr = decodeOptions([]byte(raw))
	if r.optionsErr != nil {
		slog.Warn("proxy: stored route options unreadable, route not served", "host", r.Host, "err", r.optionsErr)
	}
	return &r, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func validateTLSMode(m string) error {
	switch m {
	case "auto", "internal", "none":
		return nil
	}
	return ErrInvalidTLSMode
}
