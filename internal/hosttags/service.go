// Package hosttags provides CRUD on host_tags rows plus lookup helpers
// used by RBAC scoping (P.11.3), alert rule targeting, and backup job
// fan-out.
//
// Tags are lowercase ASCII identifiers (matching [a-z0-9-]+{1,32}).
// They attach to a host_id which is either the literal "local" string
// for the embedded docker daemon or the string form of an agents.id
// integer for remote hosts. We store host_id as TEXT so both kinds
// fit in one table without a synthetic "local host" row.
package hosttags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Errors.
var (
	ErrInvalidTag  = errors.New("invalid tag: must match [a-z0-9-]{1,32}")
	ErrTooManyTags = errors.New("too many tags: max 20 per host")
)

// tagPattern enforces the lowercase/hyphen/digit rule. We deliberately
// disallow UPPERCASE and underscore so downstream queries don't have
// to normalize — the UI shows whatever the user typed.
var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// maxTagsPerHost caps runaway tag explosion per host. 20 is generous
// for the real use cases (env + region + team + role + a few misc).
const maxTagsPerHost = 20

// Service is the CRUD layer. An in-memory cache is maintained so
// middleware-driven lookups ("does host X have tag Y?") never hit
// the DB on the hot path.
type Service struct {
	db *sql.DB

	mu    sync.RWMutex
	cache map[string]map[string]struct{} // host_id → set of tags
}

// New constructs a service. Load() populates the cache — call once at
// startup and then refresh after any mutation.
func New(db *sql.DB) *Service {
	return &Service{db: db, cache: make(map[string]map[string]struct{})}
}

// Load fills the in-memory cache from the DB. Safe to call repeatedly.
func (s *Service) Load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT host_id, tag FROM host_tags`)
	if err != nil {
		return err
	}
	defer rows.Close()

	next := make(map[string]map[string]struct{})
	for rows.Next() {
		var host, tag string
		if err := rows.Scan(&host, &tag); err != nil {
			return err
		}
		if _, ok := next[host]; !ok {
			next[host] = make(map[string]struct{})
		}
		next[host][tag] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = next
	s.mu.Unlock()
	return nil
}

// Tags returns the sorted tag list for a host. Empty slice if none.
// Fast path — reads from cache.
func (s *Service) Tags(hostID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.cache[hostID]
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// HasTag is a hot-path check used by RBAC scoping middleware.
func (s *Service) HasTag(hostID, tag string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.cache[hostID]
	if !ok {
		return false
	}
	_, hit := set[tag]
	return hit
}

// HostsWithTag returns all host IDs that have the given tag. Used by
// "apply to all prod hosts" workflows (backup scoping, alert routing).
func (s *Service) HostsWithTag(tag string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for host, set := range s.cache {
		if _, ok := set[tag]; ok {
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out
}

// HostsWithAllTags returns host IDs that have every tag in the input.
// Empty input means "any host" → returns all known host IDs in cache.
// Hosts with zero tags still match the empty query but are never
// returned for a non-empty query.
func (s *Service) HostsWithAllTags(tags []string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for host, set := range s.cache {
		match := true
		for _, t := range tags {
			if _, ok := set[t]; !ok {
				match = false
				break
			}
		}
		if match {
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out
}

// AllTags returns the global distinct set of tags across every host.
// Used to populate autocomplete in the UI.
func (s *Service) AllTags() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, set := range s.cache {
		for t := range set {
			seen[t] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Set replaces the full tag list for a host atomically. Pass an empty
// slice to remove all tags for the host. Returns the canonicalized
// (deduped, sorted) tag list actually written.
func (s *Service) Set(ctx context.Context, hostID string, tags []string) ([]string, error) {
	canon, err := canonicalize(tags)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM host_tags WHERE host_id = ?`, hostID); err != nil {
		return nil, err
	}
	for _, tag := range canon {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO host_tags (host_id, tag) VALUES (?, ?)`,
			hostID, tag,
		); err != nil {
			return nil, fmt.Errorf("insert %q: %w", tag, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return canon, s.Load(ctx)
}

// Add grants a single tag to a host. No-op if the tag is already set.
func (s *Service) Add(ctx context.Context, hostID, tag string) error {
	if !tagPattern.MatchString(tag) {
		return ErrInvalidTag
	}
	// Check cap before inserting.
	current := s.Tags(hostID)
	if len(current) >= maxTagsPerHost {
		for _, c := range current {
			if c == tag {
				return nil // already present, no-op
			}
		}
		return ErrTooManyTags
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO host_tags (host_id, tag) VALUES (?, ?)`,
		hostID, tag,
	)
	if err != nil {
		return err
	}
	return s.Load(ctx)
}

// Remove revokes a single tag from a host. No-op if the tag isn't set.
func (s *Service) Remove(ctx context.Context, hostID, tag string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM host_tags WHERE host_id = ? AND tag = ?`,
		hostID, tag,
	)
	if err != nil {
		return err
	}
	return s.Load(ctx)
}

// RemoveAllForHost drops every tag on a host. Used when a host is
// deleted so the cache doesn't retain stale associations.
func (s *Service) RemoveAllForHost(ctx context.Context, hostID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM host_tags WHERE host_id = ?`, hostID,
	)
	if err != nil {
		return err
	}
	return s.Load(ctx)
}

// canonicalize validates, deduplicates, and sorts a tag list.
func canonicalize(in []string) ([]string, error) {
	seen := make(map[string]struct{}, len(in))
	for _, t := range in {
		if !tagPattern.MatchString(t) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidTag, t)
		}
		seen[t] = struct{}{}
	}
	if len(seen) > maxTagsPerHost {
		return nil, ErrTooManyTags
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}
