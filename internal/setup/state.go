// Package setup tracks whether the server is in first-run setup mode.
//
// Setup mode is active only for an install Decide places as never set up
// or legacy unfinished. A configured install stays closed even without
// accounts or without an admin: the wizard is unauthenticated, so opening
// it on an install with data would hand that install to whoever reaches it
// first; `dockmesh admin create` recovers it instead. While
// active, the HTTP layer redirects all non-setup paths so an operator
// who just installed dockmesh and visited the dashboard URL lands on
// the install wizard rather than the login screen. After the wizard
// completes (admin user gets seeded), the in-memory state flips to
// inactive without restart, and the regular routes start answering.
//
// Nothing but the code form answers before a setup code from root
// (`dockmesh setup new-code`) opened a setup session; see Codes.
package setup

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Status is what the code form needs, and nothing more: it answers
// before any code was entered.
type Status struct {
	Active  bool         `json:"active"`
	Code    CodeState    `json:"code,omitempty"`
	Session SessionState `json:"session,omitempty"`
}

// State carries the live setup-mode flag for the lifetime of one
// server process. Built by NewForBoot at server start, then consulted
// by middleware on every request.
type State struct {
	mu         sync.RWMutex
	active     bool
	committing bool
	startedAt  time.Time
	codes      *Codes
}

// NewForced returns a State pinned to active regardless of existing users,
// with codes read from codeFile.
func NewForced(codeFile string) *State {
	return &State{active: true, startedAt: time.Now(), codes: NewCodes(codeFile)}
}

// Codes returns the setup codes of this state.
func (s *State) Codes() *Codes {
	return s.codes
}

// UsersExist reports whether the install has any user account. The wizard
// may only change an install for which it is false.
func UsersExist(ctx context.Context, db *sql.DB) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM users LIMIT 1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AdminExists reports whether some account holds an admin role
// (rbac.IsAdminPermSet) on any scope.
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

// SnapshotStatus returns the status for a request carrying token.
func (s *State) SnapshotStatus(token string) Status {
	if !s.Active() {
		return Status{}
	}
	return Status{Active: true, Code: s.codes.State(), Session: s.codes.Session(token)}
}

// TryBeginCommit claims the single install slot for the session of
// token. It returns false while another commit is running, once setup has
// finished, or when token may not write, so two concurrent wizard submits
// can't both create an admin.
func (s *State) TryBeginCommit(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.committing || !s.active || !s.codes.BeginCommit(token) {
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
	s.codes.EndCommit(success)
	if success {
		s.active = false
	}
}
