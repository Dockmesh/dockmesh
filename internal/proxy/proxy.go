// Package proxy manages a Caddy reverse-proxy container. Running Caddy
// as a managed container keeps its large dependency graph out of the
// Dockmesh binary.
//
// Caddy runs on the host network so routes can reach localhost ports,
// so a TCP admin endpoint would be open to every process on the host and
// to every container with `network_mode: host`. Its admin API therefore
// listens on a unix socket in a directory only Dockmesh's user can enter
// and only the proxy container has mounted.
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
	"github.com/dockmesh/dockmesh/internal/operations"
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
	// stored is the options column as stored; appliedHash the stateHash
	// of the row at Caddy's last successful load of it.
	stored      string
	appliedHash string
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
	// ForeignPorts lists the proxy ports another program is listening on.
	ForeignPorts []int `json:"foreign_ports,omitempty"`
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
	db      *sql.DB
	docker  *docker.Client
	enabled atomic.Bool
	// adminID is the running container's admin ID (see adminIDLabel).
	adminID atomic.Pointer[string]
	// adminSocketDir is the host directory holding the admin socket.
	adminSocketDir string
	// legacyAdmin is set while a container whose admin API could not be
	// moved to the socket keeps running on TCP.
	legacyAdmin atomic.Bool
	// foreignPorts is what GetStatus reports; foreignSeen is the previous
	// watchdog reading, only touched by the watchdog.
	foreignPorts atomic.Pointer[[]int]
	foreignSeen  []int
	notices      noticeSetter
	settings     SettingsStore

	// image is the Caddy image to run; imageOverride is set when it
	// comes from DOCKMESH_PROXY_IMAGE.
	imageMu       sync.RWMutex
	image         string
	imageOverride bool
	// shouldRun reports the stored proxy_enabled choice; set by StartWatchdog.
	shouldRun func() bool
	// startProxy replaces startAndVerify in tests.
	startProxy func(ctx context.Context) error
	// pullImage replaces ensureImage in tests.
	pullImage func(ctx context.Context, img string) error
	// imageID replaces the image lookup of isOldManagedContainer in tests.
	imageID func(ctx context.Context, ref string) string
	// inContainer reports whether Dockmesh itself runs in a container.
	inContainer func() bool

	// lifecycle serialises EnableProxy/DisableProxy, version switches
	// and settings saves; mu guards route writes, config pushes and
	// metrics.
	lifecycle sync.Mutex
	mu        sync.Mutex
	// lastLoaded is the config Caddy accepted last and lastRejected the
	// configKey of the last one it refused; both guarded by mu.
	lastLoaded   *caddyConfig
	lastRejected string
	metrics      *metricsState
	acme         *acmeBuffer

	// certMu guards the RouteCerts cache.
	certMu     sync.Mutex
	certAt     time.Time
	certRoutes []Route
	// certRefresh is closed when the running refresh ends; nil when none
	// runs.
	certRefresh chan struct{}

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
// whenever the container may run something else than the stored routes:
// at startup, after the container was created and after an admin switch.
// When Caddy refuses the stored routes while it runs the bootstrap config,
// the sites it hasn't loaded in their current state are left out.
func (s *Service) SyncFromDB(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncLocked(ctx, fallbackIfEmpty(ctx))
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
	op := operations.FromContext(ctx)
	// Before prepareOptions, which replaces basic-auth passwords by hashes.
	op.AddSecrets(opts.SecretValues()...)
	var prev *SiteOptions
	if spec.hashed {
		hashes := opts
		prev = &hashes
	}
	raw, err := s.prepareOptions(ctx, 0, spec.Host, spec.TLSMode, &opts, prev, false)
	if err != nil {
		return nil, err
	}
	op.AddSecrets(opts.SecretValues()...)
	op.StepStart(StepSave, "")
	id, outcome, err := s.applyChange(ctx, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO proxy_routes (host, upstream, tls_mode, enabled, options) VALUES (?, ?, ?, ?, ?)`,
			spec.Host, spec.Upstream, spec.TLSMode, boolToInt(!spec.Disabled), raw)
		if err != nil {
			// SQLite unique constraint code is vendor-specific; the string
			// "UNIQUE constraint" is stable enough.
			if strings.Contains(err.Error(), "UNIQUE constraint") {
				return 0, ErrDuplicateHost
			}
			return 0, err
		}
		return res.LastInsertId()
	})
	if err != nil {
		if errors.Is(err, ErrApplyRejected) {
			op.StepSkip(StepSave, "not kept")
			op.StepFail(StepApply, err)
		} else {
			op.StepFail(StepSave, err)
		}
		return nil, err
	}
	op.StepDone(StepSave, "")
	reportApply(op, outcome)
	return s.GetRoute(ctx, id)
}

// reportApply ends the apply step of a saved change.
func reportApply(op *operations.Op, outcome applyOutcome) {
	switch outcome {
	case applyOff:
		op.StepSkip(StepApply, "the proxy is off")
	case applyPending:
		op.StepSkip(StepApply, "Caddy is not reachable; the change is applied once it answers")
	default:
		op.StepDone(StepApply, "")
	}
}

// UpdateRoute replaces the upstream and TLS mode of an existing route,
// its enabled state unless enabled is nil and its options unless opts is
// nil. A disabled route stays in the table but is left out of the
// generated config, so traffic stops without losing config. It returns
// what changed (upstream, tls_mode, enabled, options.<group>) once the
// change is saved; a change Caddy refuses is not saved.
func (s *Service) UpdateRoute(ctx context.Context, id int64, upstream, tlsMode string, enabled *bool, opts *SiteOptions) ([]string, error) {
	if err := validateTLSMode(tlsMode); err != nil {
		return nil, err
	}
	if err := checkUpstream(upstream); err != nil {
		return nil, err
	}
	cur, err := s.getRoute(ctx, id)
	if err != nil {
		return nil, err
	}
	next := cur.Options
	// Kept options go through the same refill as sent ones, so secret
	// request headers are checked against a changed upstream too.
	next.Headers.RequestSet = make([]Header, len(cur.Options.Headers.RequestSet))
	for i, h := range cur.Options.Headers.RequestSet {
		next.Headers.RequestSet[i] = h
		if h.Secret {
			next.Headers.RequestSet[i].Value = ""
		}
	}
	prev := &cur.Options
	if opts != nil {
		next = *opts
	} else if cur.optionsErr != nil {
		return nil, fmt.Errorf("%w: stored options can't be read (%w); send new options", ErrInvalidOptions, cur.optionsErr)
	}
	newUpstream := addsUpstream(append([]string{cur.Upstream}, cur.Options.Routing.ExtraUpstreams...),
		append([]string{upstream}, next.Routing.ExtraUpstreams...))
	raw, err := s.prepareOptions(ctx, id, cur.Host, tlsMode, &next, prev, newUpstream)
	if err != nil {
		return nil, err
	}
	on := cur.Enabled
	if enabled != nil {
		on = *enabled
	}
	_, _, err = s.applyChange(ctx, func(tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx,
			`UPDATE proxy_routes SET upstream = ?, tls_mode = ?, enabled = ?, options = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			upstream, tlsMode, boolToInt(on), raw, id)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, ErrRouteNotFound
		}
		return id, nil
	})
	if err != nil {
		return nil, err
	}
	changed := []string{}
	if upstream != cur.Upstream {
		changed = append(changed, "upstream")
	}
	if tlsMode != cur.TLSMode {
		changed = append(changed, "tls_mode")
	}
	if on != cur.Enabled {
		changed = append(changed, "enabled")
	}
	if cur.optionsErr != nil {
		changed = append(changed, "options")
	} else {
		changed = append(changed, ChangedGroups(cur.Options, next)...)
	}
	return changed, nil
}

// addsUpstream reports whether next has an upstream address not in cur.
func addsUpstream(cur, next []string) bool {
	known := map[string]bool{}
	for _, u := range cur {
		known[strings.ToLower(u)] = true
	}
	for _, u := range next {
		if !known[strings.ToLower(u)] {
			return true
		}
	}
	return false
}

// prepareOptions hashes new passwords, fills kept secrets, validates the
// options of the site with id (0 for a new one) and returns them encoded
// for storage. newUpstream is set when the site gains an upstream address.
func (s *Service) prepareOptions(ctx context.Context, id int64, host, tlsMode string, o, prev *SiteOptions, newUpstream bool) (string, error) {
	if err := o.applyPasswords(prev); err != nil {
		return "", err
	}
	if err := o.applySecrets(prev, newUpstream); err != nil {
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
// basic-auth hashes, secret header values and the forward-auth query
// redacted and without the admin section. With mask it is built from
// Masked options.
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
	g := s.ProxySettings()
	one.Options = one.Options.withoutSecrets()
	if mask {
		one.Options = one.Options.Masked()
		g = g.Masked()
	}
	cfg, blocked := buildConfigWith([]Route{one}, g, true)
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
	_, _, err := s.applyChange(ctx, func(tx *sql.Tx) (int64, error) {
		_, err := tx.ExecContext(ctx,
			`UPDATE proxy_routes SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			boolToInt(enabled), id)
		return id, err
	})
	return err
}

// DeleteRoute removes a route.
func (s *Service) DeleteRoute(ctx context.Context, id int64) error {
	_, _, err := s.applyChange(ctx, func(tx *sql.Tx) (int64, error) {
		_, err := tx.ExecContext(ctx, `DELETE FROM proxy_routes WHERE id = ?`, id)
		return 0, err
	})
	return err
}

const routeColumns = `id, host, upstream, tls_mode, enabled, options, applied_hash, created_at, updated_at`

func (s *Service) listRoutes(ctx context.Context) ([]Route, error) {
	return queryRoutes(ctx, s.db, "")
}

// queryRoutes reads the routes matching where (empty for all), by host.
func queryRoutes(ctx context.Context, q querier, where string, args ...any) ([]Route, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+routeColumns+` FROM proxy_routes`+where+` ORDER BY host`, args...)
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
	if err := row.Scan(&r.ID, &r.Host, &r.Upstream, &r.TLSMode, &enabledInt, &r.stored, &r.appliedHash, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Enabled = enabledInt != 0
	r.Options, r.optionsErr = decodeOptions([]byte(r.stored))
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
