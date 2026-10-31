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
	"errors"
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
	// Enabled gates whether this route is included in the generated
	// Caddyfile. Disabled routes stay in the table for editing but
	// traffic stops flowing. Default true on new rows (migration 054).
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Cert metadata — populated by Service.EnrichWithCertInfo() for
	// routes whose TLS is managed by Caddy. Empty for tls_mode=none.
	CertIssuer    string     `json:"cert_issuer,omitempty"`
	CertValidFrom *time.Time `json:"cert_valid_from,omitempty"`
	CertValidTo   *time.Time `json:"cert_valid_to,omitempty"`
	CertDaysLeft  *int       `json:"cert_days_left,omitempty"`
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
	image   string
	notices noticeSetter

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
		s.image = image
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
	cfg, blocked := buildConfig(routes)
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

// ListRoutes returns all configured routes.
func (s *Service) ListRoutes(ctx context.Context) ([]Route, error) {
	return s.listRoutes(ctx)
}

// CreateRoute adds a new host → upstream mapping.
func (s *Service) CreateRoute(ctx context.Context, host, upstream, tlsMode string) (*Route, error) {
	if err := validateTLSMode(tlsMode); err != nil {
		return nil, err
	}
	if err := checkRouteHost(host); err != nil {
		return nil, err
	}
	if err := checkUpstream(upstream); err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO proxy_routes (host, upstream, tls_mode, enabled) VALUES (?, ?, ?, 1)`,
		host, upstream, tlsMode)
	if err != nil {
		// SQLite unique constraint code is vendor-specific; the string
		// "UNIQUE constraint" is stable enough.
		return nil, ErrDuplicateHost
	}
	id, _ := res.LastInsertId()
	route := &Route{ID: id, Host: host, Upstream: upstream, TLSMode: tlsMode, Enabled: true}
	if err := s.SyncFromDB(ctx); err != nil {
		return route, err
	}
	return route, nil
}

// UpdateRoute replaces the upstream, TLS mode and enabled state of an
// existing route. enabled=false keeps the row but excludes it from the
// generated Caddyfile so traffic stops flowing without losing config.
func (s *Service) UpdateRoute(ctx context.Context, id int64, upstream, tlsMode string, enabled bool) error {
	if err := validateTLSMode(tlsMode); err != nil {
		return err
	}
	if err := checkUpstream(upstream); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE proxy_routes SET upstream = ?, tls_mode = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		upstream, tlsMode, boolToInt(enabled), id)
	if err != nil {
		return err
	}
	return s.SyncFromDB(ctx)
}

// SetRouteEnabled toggles only the enabled flag — used by the row-level
// disable/enable toggle in the UI so callers don't have to round-trip
// upstream + tls_mode unchanged.
func (s *Service) SetRouteEnabled(ctx context.Context, id int64, enabled bool) error {
	if enabled {
		var host, upstream string
		err := s.db.QueryRowContext(ctx, `SELECT host, upstream FROM proxy_routes WHERE id = ?`, id).Scan(&host, &upstream)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if err := checkRouteHost(host); err != nil {
				return err
			}
			if err := checkUpstream(upstream); err != nil {
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

func (s *Service) listRoutes(ctx context.Context) ([]Route, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, host, upstream, tls_mode, enabled, created_at, updated_at FROM proxy_routes ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		var r Route
		var enabledInt int
		if err := rows.Scan(&r.ID, &r.Host, &r.Upstream, &r.TLSMode, &enabledInt, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabledInt != 0
		out = append(out, r)
	}
	return out, rows.Err()
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
