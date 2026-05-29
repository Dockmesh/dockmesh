// Package notifications backs the bell-icon notification center in
// the top-bar. Producer code (gitDeploy callback, alert evaluator,
// backup runner, …) calls Emit() to drop a row; the UI polls
// /notifications and shows unread-count + a side drawer.
//
// Architecture: every row carries a concrete users.id. There are no
// shared "broadcast" rows. Producers that want "everyone with the
// admin role should see this" pass an empty UserID; Emit fans out at
// write time into one row per user. This is the GitHub / Portainer
// Business pattern — read state is naturally per-user, deletions are
// scoped, and new sign-ups never see historical notifications because
// no rows were ever created for them.
package notifications

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"
)

// UserDirectory is the subset of auth.Service that Emit needs for
// fan-out: a list of every active user id the server knows about.
// Injected via SetUserDirectory so the notifications package doesn't
// import auth (auth is the bigger graph, easier to keep one-way).
type UserDirectory interface {
	ListActiveUserIDs(ctx context.Context) ([]string, error)
}

// Severity drives the badge color in the UI.
type Severity string

const (
	SevInfo    Severity = "info"
	SevSuccess Severity = "success"
	SevWarning Severity = "warning"
	SevError   Severity = "error"
)

// Kind is a stable identifier the UI maps to an icon + filter group.
// Add new kinds here when a new producer ships so the frontend can
// recognize them.
type Kind string

const (
	KindDeployOK         Kind = "deploy.ok"
	KindDeployFail       Kind = "deploy.fail"
	KindAlertFire        Kind = "alert.fire"
	KindBackupOK         Kind = "backup.ok"
	KindBackupFail       Kind = "backup.fail"
	KindImageUpdate      Kind = "image.update"
	KindAgentOffline     Kind = "agent.offline"
	KindSystemUpgrade    Kind = "system.upgrade"
	KindMigrationOK      Kind = "migration.complete"
	KindMigrationFail    Kind = "migration.failed"
	KindCVEFound         Kind = "cve.found"
	KindInviteAccepted   Kind = "invite.accepted"
)

// Notification is one row from the table.
type Notification struct {
	ID        int64      `json:"id"`
	UserID    string     `json:"user_id"`
	Kind      Kind       `json:"kind"`
	Severity  Severity   `json:"severity"`
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Link      string     `json:"link,omitempty"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// EmitInput is the producer-facing shape — keeps Emit() readable when
// callers fill out half the fields. UserID="" means "fan out to every
// user known to the directory" (i.e. what used to be a broadcast).
type EmitInput struct {
	UserID   string
	Kind     Kind
	Severity Severity
	Title    string
	Body     string
	Link     string
}

var ErrNotFound = errors.New("notification not found")

type Service struct {
	db    *sql.DB
	users UserDirectory
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// SetUserDirectory wires in the user lookup post-construction. The
// auth service is constructed before notifications, but the directory
// lookup is only used by Emit — wiring after both exist avoids the
// cycle. Nil = degrade Emit to "ignore fan-out, log a warning".
func (s *Service) SetUserDirectory(d UserDirectory) {
	s.users = d
}

// Emit drops a notification. If UserID is set, one row is written. If
// UserID is empty, the user directory is asked for every active user
// and one row is written per user — the GitHub-style fan-out pattern
// so read-state is naturally per-user and a brand-new account never
// sees notifications emitted before it existed.
//
// Returns the first inserted row id when fan-out runs (most useful for
// audit/log lines; callers that care about every row should iterate
// themselves). Severity defaults to "info" if unset.
func (s *Service) Emit(ctx context.Context, in EmitInput) (int64, error) {
	if in.Severity == "" {
		in.Severity = SevInfo
	}
	if in.UserID != "" {
		return s.insertOne(ctx, in.UserID, in)
	}
	// Fan-out path. Ask the directory for active users and insert one
	// row per user. Failures of individual inserts are logged and the
	// loop continues — losing one fan-out row is better than dropping
	// the entire emit.
	if s.users == nil {
		slog.Warn("notification fan-out skipped — user directory not wired", "kind", in.Kind, "title", in.Title)
		return 0, nil
	}
	ids, err := s.users.ListActiveUserIDs(ctx)
	if err != nil {
		return 0, err
	}
	var firstID int64
	for _, uid := range ids {
		id, err := s.insertOne(ctx, uid, in)
		if err != nil {
			slog.Warn("notification fan-out insert failed", "user", uid, "err", err)
			continue
		}
		if firstID == 0 {
			firstID = id
		}
	}
	return firstID, nil
}

// insertOne writes a single per-user row. Called both directly (when
// UserID is set on EmitInput) and from the fan-out loop.
func (s *Service) insertOne(ctx context.Context, userID string, in EmitInput) (int64, error) {
	var bodyArg sql.NullString
	if in.Body != "" {
		bodyArg = sql.NullString{String: in.Body, Valid: true}
	}
	var linkArg sql.NullString
	if in.Link != "" {
		linkArg = sql.NullString{String: in.Link, Valid: true}
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO notifications (user_id, kind, severity, title, body, link)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, string(in.Kind), string(in.Severity), in.Title, bodyArg, linkArg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// List returns the most recent notifications for a user, newest
// first. user_id is required and matched exactly — there are no shared
// broadcast rows in the schema anymore (see migration 057).
func (s *Service) List(ctx context.Context, userID string, unreadOnly bool, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, user_id, kind, severity, title,
	             COALESCE(body, ''), COALESCE(link, ''), read_at, created_at
	      FROM notifications
	      WHERE user_id = ?`
	args := []any{userID}
	if unreadOnly {
		q += " AND read_at IS NULL"
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Notification, 0)
	for rows.Next() {
		var n Notification
		var readAt sql.NullTime
		var kind, severity string
		if err := rows.Scan(&n.ID, &n.UserID, &kind, &severity, &n.Title,
			&n.Body, &n.Link, &readAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		n.Kind = Kind(kind)
		n.Severity = Severity(severity)
		if readAt.Valid {
			t := readAt.Time
			n.ReadAt = &t
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCount is the bell-badge number. Per-user only — every row
// belongs to exactly one user since migration 057.
func (s *Service) UnreadCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM notifications
		 WHERE user_id = ? AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkRead sets read_at on a row. Caller must own the row OR be reading
// a broadcast — we don't enforce that here, the handler does.
func (s *Service) MarkRead(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET read_at = CURRENT_TIMESTAMP
		   WHERE id = ? AND read_at IS NULL`, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		// Already-read OR doesn't exist — both are fine, idempotent.
	}
	return nil
}

// MarkAllRead clears the unread badge for one user. Touches only that
// user's rows — fan-out at emit time means each user has their own
// copy to mark.
func (s *Service) MarkAllRead(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notifications SET read_at = CURRENT_TIMESTAMP
		 WHERE user_id = ? AND read_at IS NULL`, userID)
	return err
}

// Delete removes a row. Hard-delete: no soft-delete column. Use when
// a notification is no longer relevant (e.g. user dismisses it from
// the drawer).
func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notifications WHERE id = ?`, id)
	return err
}

// Get fetches a single notification by id. Used by the MarkRead
// handler for an ownership check before mutating.
func (s *Service) Get(ctx context.Context, id int64) (*Notification, error) {
	var n Notification
	var readAt sql.NullTime
	var kind, severity string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(user_id, ''), kind, severity, title,
		       COALESCE(body, ''), COALESCE(link, ''), read_at, created_at
		FROM notifications WHERE id = ?`, id).Scan(
		&n.ID, &n.UserID, &kind, &severity, &n.Title,
		&n.Body, &n.Link, &readAt, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	n.Kind = Kind(kind)
	n.Severity = Severity(severity)
	if readAt.Valid {
		t := readAt.Time
		n.ReadAt = &t
	}
	return &n, nil
}
