// Package settings provides a DB-backed key-value store for runtime-
// configurable system settings. Values are cached in memory and
// refreshed on write so reads are zero-cost.
package settings

import (
	"context"
	"database/sql"
	"os"
	"sync"
)

// Known setting keys.
const (
	KeyProxyEnabled    = "proxy_enabled"
	KeyScannerEnabled  = "scanner_enabled"
	KeyBaseURL         = "base_url"
	KeyAgentPublicURL  = "agent_public_url"
)

// Entry is one row in the settings table.
type Entry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Store provides read/write access to the settings table with an
// in-memory cache. Reads never hit the DB after Load().
type Store struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]string
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, cache: make(map[string]string)}
}

// Load populates the cache from the DB. Called once at startup.
// For each key, if the DB value is empty, check the corresponding
// env var so existing .env-based installs migrate seamlessly.
func (s *Store) Load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		// If DB value is empty, try env var as migration path.
		if v == "" || v == "false" {
			if envVal := envForKey(k); envVal != "" {
				v = envVal
				// Persist so next boot uses DB value.
				_, _ = s.db.ExecContext(ctx,
					`UPDATE settings SET value = ?, updated_at = CURRENT_TIMESTAMP WHERE key = ?`, v, k)
			}
		}
		s.cache[k] = v
	}
	return rows.Err()
}

// Get returns a setting value from cache. Falls back to env var,
// then to the provided default.
func (s *Store) Get(key, def string) string {
	s.mu.RLock()
	v, ok := s.cache[key]
	s.mu.RUnlock()
	if ok && v != "" {
		return v
	}
	if env := envForKey(key); env != "" {
		return env
	}
	return def
}

// GetBool is a convenience for boolean settings.
func (s *Store) GetBool(key string, def bool) bool {
	v := s.Get(key, "")
	if v == "" {
		return def
	}
	return v == "true" || v == "1" || v == "yes"
}

// Set writes a setting to DB + cache.
func (s *Store) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache[key] = value
	s.mu.Unlock()
	return nil
}

// All returns every setting for the API.
func (s *Store) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.cache))
	for k, v := range s.cache {
		out = append(out, Entry{Key: k, Value: v})
	}
	return out
}

// envForKey maps a setting key to its legacy env var name.
func envForKey(key string) string {
	envMap := map[string]string{
		KeyProxyEnabled:   "DOCKMESH_PROXY_ENABLED",
		KeyScannerEnabled: "DOCKMESH_SCANNER_ENABLED",
		KeyBaseURL:        "DOCKMESH_BASE_URL",
		KeyAgentPublicURL: "DOCKMESH_AGENT_PUBLIC_URL",
	}
	if envName, ok := envMap[key]; ok {
		return os.Getenv(envName)
	}
	return ""
}
