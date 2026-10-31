// Package userprefs stores small per-user preferences.
package userprefs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// KeyWhatsNewSeen is the last version whose release notes the user saw.
const KeyWhatsNewSeen = "whats_new_seen"

// Store reads and writes user_preferences.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// Get returns the user's value for key, or "" when none is stored.
func (s *Store) Get(ctx context.Context, userID, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM user_preferences WHERE user_id = ? AND key = ?`, userID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get preference %s: %w", key, err)
	}
	return v, nil
}

// Set stores value for the user's key.
func (s *Store) Set(ctx context.Context, userID, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_preferences (user_id, key, value, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		userID, key, value)
	if err != nil {
		return fmt.Errorf("set preference %s: %w", key, err)
	}
	return nil
}
