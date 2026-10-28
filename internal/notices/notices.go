// Package notices holds action-required notices: conditions that need a
// person to do something (typically after an upgrade), shown as a banner and
// at the top of the bell drawer until the condition is gone.
//
// Unlike notifications, a notice is one row per condition, keyed by a stable
// string. Producers Raise it while the condition holds and Resolve it once it
// doesn't; raising an open key again is a no-op. The audience is evaluated
// when a user reads, so it follows role changes and new accounts.
package notices

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

type Severity string

const (
	SevInfo     Severity = "info"
	SevWarning  Severity = "warning"
	SevCritical Severity = "critical"
)

type AudienceKind string

const (
	AudienceAll        AudienceKind = "all"
	AudienceAdmins     AudienceKind = "admins"
	AudienceUsers      AudienceKind = "users"
	AudiencePermission AudienceKind = "permission"
)

// Audience selects who sees a notice. UserIDs is used with AudienceUsers,
// Permission with AudiencePermission. AudienceAdmins means "may change
// system settings" (AdminPermission), so custom roles holding it count and
// a role merely named "admin" without it doesn't.
type Audience struct {
	Kind       AudienceKind
	UserIDs    []string
	Permission string
}

// AdminPermission defines the admins audience.
const AdminPermission = string(rbac.PermSystemUpdate)

func All() Audience                    { return Audience{Kind: AudienceAll} }
func Admins() Audience                 { return Audience{Kind: AudienceAdmins} }
func Users(ids ...string) Audience     { return Audience{Kind: AudienceUsers, UserIDs: ids} }
func WithPermission(p string) Audience { return Audience{Kind: AudiencePermission, Permission: p} }

type Notice struct {
	ID          int64      `json:"id"`
	Key         string     `json:"key"`
	Severity    Severity   `json:"severity"`
	Title       string     `json:"title"`
	Body        string     `json:"body,omitempty"`
	Link        string     `json:"link,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Dismissible bool       `json:"dismissible"`
	Dismissed   bool       `json:"dismissed"`

	audience Audience
}

// RaiseInput describes a notice. Body is plain text; Link is an in-app path
// ("/stacks/foo") the UI turns into a button.
type RaiseInput struct {
	Key      string
	Audience Audience
	Severity Severity
	Title    string
	Body     string
	Link     string
}

// Viewer is the reading user. Allowed answers permission checks for the
// viewer's role (custom roles included).
type Viewer struct {
	UserID  string
	Role    string
	Allowed func(perm string) bool
}

var (
	ErrNotFound       = errors.New("notice not found")
	ErrNotDismissible = errors.New("critical notices cannot be dismissed")
)

// Check is a condition evaluated at startup, after migrations and service
// wiring. It raises or resolves its notices through the service.
type Check func(ctx context.Context, s *Service) error

type Service struct {
	db *sql.DB

	mu     sync.Mutex
	checks []namedCheck
}

type namedCheck struct {
	name string
	fn   Check
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

func (in RaiseInput) validate() error {
	if strings.TrimSpace(in.Key) == "" {
		return errors.New("notice key is required")
	}
	if strings.TrimSpace(in.Title) == "" {
		return errors.New("notice title is required")
	}
	switch in.Severity {
	case SevInfo, SevWarning, SevCritical:
	default:
		return fmt.Errorf("invalid notice severity %q", in.Severity)
	}
	switch in.Audience.Kind {
	case AudienceAll, AudienceAdmins:
	case AudienceUsers:
		if len(in.Audience.UserIDs) == 0 {
			return errors.New("users audience needs at least one user id")
		}
	case AudiencePermission:
		if in.Audience.Permission == "" {
			return errors.New("permission audience needs a permission")
		}
	default:
		return fmt.Errorf("invalid notice audience %q", in.Audience.Kind)
	}
	// Only in-app paths: an absolute or protocol-relative URL here would
	// turn a notice into an off-site link.
	if in.Link != "" && (!strings.HasPrefix(in.Link, "/") || strings.HasPrefix(in.Link, "//") || strings.ContainsAny(in.Link, "\\\r\n")) {
		return fmt.Errorf("notice link must be an in-app path, got %q", in.Link)
	}
	return nil
}

func encodeAudienceValue(a Audience) (string, error) {
	switch a.Kind {
	case AudienceUsers:
		b, err := json.Marshal(a.UserIDs)
		return string(b), err
	case AudiencePermission:
		return a.Permission, nil
	}
	return "", nil
}

func decodeAudience(kind, value string) Audience {
	a := Audience{Kind: AudienceKind(kind)}
	switch a.Kind {
	case AudienceUsers:
		_ = json.Unmarshal([]byte(value), &a.UserIDs)
	case AudiencePermission:
		a.Permission = value
	}
	return a
}

// Raise opens the notice for in.Key. An open notice with that key is left
// untouched; a resolved one is reopened with the new content and its
// dismissals cleared, since the condition came back.
func (s *Service) Raise(ctx context.Context, in RaiseInput) error {
	if err := in.validate(); err != nil {
		return err
	}
	value, err := encodeAudienceValue(in.Audience)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	var resolved sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT id, resolved_at FROM notices WHERE key = ?`, in.Key).Scan(&id, &resolved)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO notices (key, audience, audience_value, severity, title, body, link)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			in.Key, string(in.Audience.Kind), value, string(in.Severity), in.Title, in.Body, in.Link); err != nil {
			return err
		}
	case err != nil:
		return err
	case !resolved.Valid:
		return nil
	default:
		if _, err := tx.ExecContext(ctx, `
			UPDATE notices SET audience = ?, audience_value = ?, severity = ?, title = ?, body = ?, link = ?,
			       created_at = CURRENT_TIMESTAMP, resolved_at = NULL
			 WHERE id = ?`,
			string(in.Audience.Kind), value, string(in.Severity), in.Title, in.Body, in.Link, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM notice_dismissals WHERE notice_id = ?`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("notice raised", "key", in.Key, "severity", in.Severity)
	return nil
}

// Resolve closes the open notice for key. Unknown or already resolved keys
// are fine, so producers can call it unconditionally.
func (s *Service) Resolve(ctx context.Context, key string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE notices SET resolved_at = CURRENT_TIMESTAMP WHERE key = ? AND resolved_at IS NULL`, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("notice resolved", "key", key)
	}
	return nil
}

// Set raises or resolves key depending on whether the condition holds.
func (s *Service) Set(ctx context.Context, active bool, in RaiseInput) error {
	if active {
		return s.Raise(ctx, in)
	}
	return s.Resolve(ctx, in.Key)
}

// Open returns every unresolved notice, without audience filtering.
func (s *Service) Open(ctx context.Context) ([]Notice, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, key, audience, audience_value, severity, title, body, link, created_at
		  FROM notices WHERE resolved_at IS NULL
		 ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Notice, 0)
	for rows.Next() {
		var n Notice
		var kind, value, sev string
		if err := rows.Scan(&n.ID, &n.Key, &kind, &value, &sev, &n.Title, &n.Body, &n.Link, &n.CreatedAt); err != nil {
			return nil, err
		}
		n.Severity = Severity(sev)
		n.Dismissible = n.Severity != SevCritical
		n.audience = decodeAudience(kind, value)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (a Audience) includes(v Viewer) bool {
	switch a.Kind {
	case AudienceAll:
		return true
	case AudienceAdmins:
		return v.Allowed != nil && v.Allowed(AdminPermission)
	case AudienceUsers:
		for _, id := range a.UserIDs {
			if id != "" && id == v.UserID {
				return true
			}
		}
	case AudiencePermission:
		return v.Allowed != nil && v.Allowed(a.Permission)
	}
	return false
}

// ForViewer returns the open notices v is in the audience of, with
// Dismissed set from v's dismissals.
func (s *Service) ForViewer(ctx context.Context, v Viewer) ([]Notice, error) {
	open, err := s.Open(ctx)
	if err != nil {
		return nil, err
	}
	dismissed := map[int64]bool{}
	if v.UserID != "" {
		rows, err := s.db.QueryContext(ctx, `SELECT notice_id FROM notice_dismissals WHERE user_id = ?`, v.UserID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			dismissed[id] = true
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]Notice, 0, len(open))
	for _, n := range open {
		if !n.audience.includes(v) {
			continue
		}
		n.Dismissed = dismissed[n.ID]
		out = append(out, n)
	}
	return out, nil
}

// Dismiss hides an open, non-critical notice from v's banner. It stays in
// the bell drawer until resolved.
func (s *Service) Dismiss(ctx context.Context, v Viewer, id int64) error {
	if v.UserID == "" {
		return ErrNotFound
	}
	visible, err := s.ForViewer(ctx, v)
	if err != nil {
		return err
	}
	for _, n := range visible {
		if n.ID != id {
			continue
		}
		if !n.Dismissible {
			return ErrNotDismissible
		}
		_, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO notice_dismissals (notice_id, user_id) VALUES (?, ?)`, id, v.UserID)
		return err
	}
	return ErrNotFound
}

// Register adds a startup check. Call before RunChecks.
func (s *Service) Register(name string, fn Check) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks = append(s.checks, namedCheck{name: name, fn: fn})
}

// RunChecks runs every registered check. A failing check is logged and the
// rest still run; one broken condition must not keep the server from
// starting.
func (s *Service) RunChecks(ctx context.Context) {
	s.mu.Lock()
	checks := append([]namedCheck(nil), s.checks...)
	s.mu.Unlock()
	for _, c := range checks {
		if err := c.fn(ctx, s); err != nil {
			slog.Warn("notice check failed", "check", c.name, "err", err)
		}
	}
}
