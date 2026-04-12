// Package audit records who did what when. Phase 1 is append-only via
// application logic — no hash chain yet (§15.10 Phase 2).
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

// Action constants so callers don't fat-finger strings.
const (
	ActionLogin          = "auth.login"
	ActionLoginFailed    = "auth.login_failed"
	ActionLogout         = "auth.logout"
	ActionRefresh        = "auth.refresh"
	ActionUserCreate     = "user.create"
	ActionUserUpdate     = "user.update"
	ActionUserDelete     = "user.delete"
	ActionUserPassword   = "user.password"
	ActionStackCreate    = "stack.create"
	ActionStackUpdate    = "stack.update"
	ActionStackDelete    = "stack.delete"
	ActionStackDeploy    = "stack.deploy"
	ActionStackStop      = "stack.stop"
	ActionContainerStart = "container.start"
	ActionContainerStop  = "container.stop"
	ActionContainerKill  = "container.restart"
	ActionContainerRm    = "container.remove"
	ActionImagePull      = "image.pull"
	ActionImageRemove    = "image.remove"
	ActionImagePrune     = "image.prune"
	ActionNetworkCreate  = "network.create"
	ActionNetworkRemove  = "network.remove"
	ActionVolumeCreate   = "volume.create"
	ActionVolumeRemove   = "volume.remove"
	ActionVolumePrune    = "volume.prune"
)

type Entry struct {
	ID      int64     `json:"id"`
	TS      time.Time `json:"ts"`
	UserID  string    `json:"user_id,omitempty"`
	Action  string    `json:"action"`
	Target  string    `json:"target,omitempty"`
	Details string    `json:"details,omitempty"`
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

// Write records an audit entry. Failures are logged but never block the
// caller — audit writes must not break the main request flow.
func (s *Service) Write(ctx context.Context, userID, action, target string, details any) {
	var detailStr string
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			detailStr = string(b)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (user_id, action, target, details) VALUES (?, ?, ?, ?)`,
		nullable(userID), action, nullable(target), nullable(detailStr),
	)
	if err != nil {
		slog.Warn("audit write failed", "err", err, "action", action)
	}
}

// List returns the most recent entries, newest first.
func (s *Service) List(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, user_id, action, target, details
		   FROM audit_log
		  ORDER BY id DESC
		  LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		var userID, target, details sql.NullString
		if err := rows.Scan(&e.ID, &e.TS, &userID, &e.Action, &target, &details); err != nil {
			return nil, err
		}
		if userID.Valid {
			e.UserID = userID.String
		}
		if target.Valid {
			e.Target = target.String
		}
		if details.Valid {
			e.Details = details.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
