// Package setup tracks whether the server is in first-run setup mode.
//
// Setup mode is active when the database has zero admin users. While
// active, the HTTP layer redirects all non-setup paths so an operator
// who just installed dockmesh and visited the dashboard URL lands on
// the install wizard rather than the login screen. After the wizard
// completes (admin user gets seeded), the in-memory state flips to
// inactive without restart, and the regular routes start answering.
//
// A 30-minute window from server start protects against drive-by
// attacks where someone in the LAN beats the legitimate operator to
// the wizard. After the window expires, the wizard's "create admin"
// endpoint returns 410 and refuses to seed; the operator restarts the
// service to get a fresh window. This matches Portainer's first-run
// safety pattern.
package setup

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

// SetupWindow is how long after server start the wizard accepts a
// "complete setup" request. After this, the operator restarts the
// service to get a fresh window.
const SetupWindow = 30 * time.Minute

// Status is the JSON shape returned to the wizard frontend so it can
// decide what to render.
type Status struct {
	Active     bool      `json:"active"`
	StartedAt  time.Time `json:"started_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Expired    bool      `json:"expired"`
	WindowSecs int       `json:"window_secs"`
}

// State carries the live setup-mode flag for the lifetime of one
// server process. Built by main at boot from the DB, then consulted
// by middleware on every request.
type State struct {
	mu        sync.RWMutex
	active    bool
	startedAt time.Time
}

// NewForced returns a State pinned to active. Used during development
// or when the install script signals via DOCKMESH_SETUP_FORCE=true
// that the wizard owns admin creation regardless of existing rows.
func NewForced() *State {
	return &State{active: true, startedAt: time.Now()}
}

// NewFromDB inspects the users table and decides whether setup mode
// should be active. Setup is active when no admin users exist. The
// returned State records "now" as the start of the wizard window.
func NewFromDB(ctx context.Context, db *sql.DB) (*State, error) {
	if db == nil {
		// No DB — treat as setup-mode-on so the wizard at least loads.
		return &State{active: true, startedAt: time.Now()}, nil
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n); err != nil {
		return nil, err
	}
	return &State{active: n == 0, startedAt: time.Now()}, nil
}

// Active reports whether requests should be gated to /setup paths.
func (s *State) Active() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// StartedAt returns the moment the current wizard window opened.
func (s *State) StartedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.startedAt
}

// Expired returns true when the setup window has elapsed without the
// wizard being completed. The window protects against drive-by
// admin-creation if the legitimate operator wandered off.
func (s *State) Expired() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.active {
		return false
	}
	return time.Since(s.startedAt) > SetupWindow
}

// SnapshotStatus returns a JSON-friendly status struct.
func (s *State) SnapshotStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	exp := s.startedAt.Add(SetupWindow)
	return Status{
		Active:     s.active,
		StartedAt:  s.startedAt,
		ExpiresAt:  exp,
		Expired:    s.active && time.Now().After(exp),
		WindowSecs: int(SetupWindow.Seconds()),
	}
}

// Complete flips the state to inactive. Called by the wizard's submit
// handler after the admin user has been written to the DB. After this,
// the gating middleware lets all routes through normally and a future
// process restart will not re-enter setup mode (the DB has admins now).
func (s *State) Complete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = false
}
