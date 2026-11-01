// Package setup tracks whether the server is in first-run setup mode.
//
// Setup mode is active when no user can administer the install. While
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

	"github.com/dockmesh/dockmesh/internal/rbac"
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
	mu         sync.RWMutex
	active     bool
	committing bool
	startedAt  time.Time
}

// NewForced returns a State pinned to active regardless of existing users.
func NewForced() *State {
	return &State{active: true, startedAt: time.Now()}
}

// NewFromDB inspects the users table and decides whether setup mode
// should be active. Setup is active when no user holds the permissions to
// administer the install, whatever their role is called. The returned
// State records "now" as the start of the wizard window.
func NewFromDB(ctx context.Context, db *sql.DB) (*State, error) {
	if db == nil {
		// No DB — treat as setup-mode-on so the wizard at least loads.
		return &State{active: true, startedAt: time.Now()}, nil
	}
	ok, err := AdminExists(ctx, db)
	if err != nil {
		return nil, err
	}
	return &State{active: !ok, startedAt: time.Now()}, nil
}

// AdminExists reports whether some account holds an admin role
// (rbac.IsAdminPermSet). Any scope counts: an upgraded install whose
// admins ended up on narrower scopes must not reopen the unauthenticated
// wizard; `dockmesh admin create` recovers it.
func AdminExists(ctx context.Context, db *sql.DB) (bool, error) {
	roles, err := rbac.LoadRoles(ctx, db)
	if err != nil {
		return false, err
	}
	admin := map[string]bool{}
	for _, r := range roles {
		admin[r.Name] = rbac.IsAdminPermSet(rbac.PermsFor(r.Levels, r.Extras))
	}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT role_name FROM role_assignments`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return false, err
		}
		if admin[role] {
			return true, nil
		}
	}
	return false, rows.Err()
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

// TryBeginCommit claims the single install slot. It returns false while
// another commit is running or once setup has finished or expired, so two
// concurrent wizard submits can't both create an admin.
func (s *State) TryBeginCommit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.committing || !s.active || time.Since(s.startedAt) > SetupWindow {
		return false
	}
	s.committing = true
	return true
}

// EndCommit releases the install slot. A successful install also ends
// setup mode, in the same critical section, so no third commit can slip in
// between the two.
func (s *State) EndCommit(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.committing = false
	if success {
		s.active = false
	}
}
