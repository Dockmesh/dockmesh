package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/docker/docker/errdefs"
)

// SettingsStore is the part of the settings store the boot decision needs.
type SettingsStore interface {
	Get(key, def string) string
	Set(ctx context.Context, key, value string) error
}

// KeyEnabled is the settings key holding the operator's proxy on/off choice.
const KeyEnabled = "proxy_enabled"

// Reconcile brings the proxy into the state the operator chose before the
// restart and pushes the stored routes. envDefault is the fallback when the
// setting was never saved.
//
// When the setting is unset but a managed container exists that carries
// routes or has routes waiting in the database, the proxy was in use: it is
// treated as enabled and that choice is saved.
func (s *Service) Reconcile(ctx context.Context, st SettingsStore, envDefault bool) (bool, error) {
	enabled := envDefault
	if raw := st.Get(KeyEnabled, ""); raw != "" {
		enabled = raw == "true" || raw == "1" || raw == "yes"
	} else if !envDefault && s.inUse(ctx) {
		enabled = true
		if err := st.Set(ctx, KeyEnabled, "true"); err != nil {
			slog.Warn("proxy: saving adopted proxy_enabled failed", "err", err)
		} else {
			slog.Info("proxy: managed container found with routes, treating the proxy as enabled")
		}
	}
	if !enabled {
		return false, nil
	}
	_, running := s.state(ctx)
	if running {
		if ok, _ := adminStatus(ctx); ok {
			s.mu.Lock()
			s.enabled = true
			routes, err := s.listRoutes(ctx)
			if err == nil {
				err = s.pushConfig(ctx, routes)
			}
			s.mu.Unlock()
			return true, err
		}
	}
	return true, s.EnableProxy(ctx)
}

func (s *Service) state(ctx context.Context) (exists, running bool) {
	if s.containerState != nil {
		return s.containerState(ctx)
	}
	if s.docker == nil {
		return false, false
	}
	info, err := s.docker.Raw().ContainerInspect(ctx, ProxyContainerName)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			slog.Warn("proxy: container inspect failed", "err", err)
		}
		return false, false
	}
	return true, info.State != nil && info.State.Running
}

func (s *Service) inUse(ctx context.Context) bool {
	exists, _ := s.state(ctx)
	if !exists {
		return false
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_routes WHERE enabled = 1`).Scan(&n); err == nil && n > 0 {
		return true
	}
	return caddyHasRoutes(ctx)
}

func caddyHasRoutes(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/config/apps/http/servers", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false
	}
	var servers map[string]struct {
		Routes []json.RawMessage `json:"routes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
		return false
	}
	for _, sv := range servers {
		if len(sv.Routes) > 0 {
			return true
		}
	}
	return false
}
