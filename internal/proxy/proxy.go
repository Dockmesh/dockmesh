// Package proxy manages a Caddy reverse-proxy container (§2.6). The
// concept calls for embedded Caddy but the Go library has a huge dep
// graph; running Caddy as a managed docker container is the pragmatic
// MVP choice and can be swapped for an embedded impl later behind the
// same Service interface.
package proxy

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
)

// Route is a single reverse-proxy entry. Host is matched against the
// incoming request and traffic is forwarded to Upstream.
type Route struct {
	ID        int64     `json:"id"`
	Host      string    `json:"host"`
	Upstream  string    `json:"upstream"`
	TLSMode   string    `json:"tls_mode"` // auto | internal | none
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

type Service struct {
	db      *sql.DB
	docker  *docker.Client
	enabled bool

	mu      sync.Mutex
	metrics *metricsState
	acme    *acmeBuffer
}

func NewService(db *sql.DB, dockerCli *docker.Client, enabled bool) *Service {
	return &Service{db: db, docker: dockerCli, enabled: enabled}
}

func (s *Service) Enabled() bool { return s.enabled }

// SyncFromDB loads all routes and pushes a fresh config to Caddy. Called
// at startup and after every mutation.
func (s *Service) SyncFromDB(ctx context.Context) error {
	if !s.enabled {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	routes, err := s.listRoutes(ctx)
	if err != nil {
		return err
	}
	return s.pushConfig(ctx, routes)
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
