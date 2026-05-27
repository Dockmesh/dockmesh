// Package notifications backs the bell-icon notification center in
// the top-bar. Producer code (gitDeploy callback, alert evaluator,
// backup runner, …) calls Emit() to drop a row; the UI polls
// /notifications and shows unread-count + a side drawer.
//
// Per-user vs broadcast: pass userID="" for broadcast (everyone sees
// it); pass a concrete users.id for per-user (only that user's
// dashboard shows it). The list query unions both.
package notifications

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

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
	KindDeployOK      Kind = "deploy.ok"
	KindDeployFail    Kind = "deploy.fail"
	KindAlertFire     Kind = "alert.fire"
	KindBackupOK      Kind = "backup.ok"
	KindBackupFail    Kind = "backup.fail"
	KindImageUpdate   Kind = "image.update"
	KindAgentOffline  Kind = "agent.offline"
	KindSystemUpgrade Kind = "system.upgrade"
)

// Notification is one row from the table.
type Notification struct {
	ID        int64      `json:"id"`
	UserID    string     `json:"user_id,omitempty"` // empty = broadcast
	Kind      Kind       `json:"kind"`
	Severity  Severity   `json:"severity"`
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Link      string     `json:"link,omitempty"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// EmitInput is the producer-facing shape — keeps Emit() readable when
// callers fill out half the fields.
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
	db *sql.DB
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Emit drops a new notification. Returns the row id. UserID empty =
// broadcast (every user sees it on their list). Severity defaults to
// "info" if unset.
func (s *Service) Emit(ctx context.Context, in EmitInput) (int64, error) {
	if in.Severity == "" {
		in.Severity = SevInfo
	}
	var userArg sql.NullString
	if in.UserID != "" {
		userArg = sql.NullString{String: in.UserID, Valid: true}
	}
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
		userArg, string(in.Kind), string(in.Severity), in.Title, bodyArg, linkArg)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// List returns the most recent notifications for a user — both
// own + broadcast — newest first. When unreadOnly=true, filters to
// rows with read_at IS NULL.
func (s *Service) List(ctx context.Context, userID string, unreadOnly bool, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, COALESCE(user_id, ''), kind, severity, title,
	             COALESCE(body, ''), COALESCE(link, ''), read_at, created_at
	      FROM notifications
	      WHERE (user_id = ? OR user_id IS NULL)`
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

// UnreadCount is the bell-badge number. Counts both own + broadcast
// rows where the caller hasn't acknowledged yet.
func (s *Service) UnreadCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM notifications
		 WHERE (user_id = ? OR user_id IS NULL)
		   AND read_at IS NULL`, userID).Scan(&n)
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

// MarkAllRead clears the unread badge for a user. Touches both own +
// broadcast rows the user can see.
func (s *Service) MarkAllRead(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE notifications SET read_at = CURRENT_TIMESTAMP
		 WHERE (user_id = ? OR user_id IS NULL)
		   AND read_at IS NULL`, userID)
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
