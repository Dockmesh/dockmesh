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
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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

	mu sync.Mutex
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
		`INSERT INTO proxy_routes (host, upstream, tls_mode) VALUES (?, ?, ?)`,
		host, upstream, tlsMode)
	if err != nil {
		// SQLite unique constraint code is vendor-specific; the string
		// "UNIQUE constraint" is stable enough.
		return nil, ErrDuplicateHost
	}
	id, _ := res.LastInsertId()
	route := &Route{ID: id, Host: host, Upstream: upstream, TLSMode: tlsMode}
	if err := s.SyncFromDB(ctx); err != nil {
		return route, err
	}
	return route, nil
}

// UpdateRoute replaces the upstream and TLS mode of an existing route.
func (s *Service) UpdateRoute(ctx context.Context, id int64, upstream, tlsMode string) error {
	if err := validateTLSMode(tlsMode); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE proxy_routes SET upstream = ?, tls_mode = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		upstream, tlsMode, id)
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
		`SELECT id, host, upstream, tls_mode, created_at, updated_at FROM proxy_routes ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		var r Route
		if err := rows.Scan(&r.ID, &r.Host, &r.Upstream, &r.TLSMode, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func validateTLSMode(m string) error {
	switch m {
	case "auto", "internal", "none":
		return nil
	}
	return ErrInvalidTLSMode
}
