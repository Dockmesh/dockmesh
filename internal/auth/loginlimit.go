package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// Per-account sign-in limit: password and two-factor failures for one
// username share it, and it applies whatever the lockout policy says.
// Counters of existing users live on their users row, so every unlock path
// clears them, including `dockmesh admin unlock`, which writes the database
// without going through the server. Names that match no user are counted in
// memory under the same rules, so they answer the same way.
const (
	AccountLimitMax    = 10
	AccountLimitWindow = 15 * time.Minute
	AccountLimitBlock  = 15 * time.Minute
)

// ErrTooManyAttempts is returned while an account is blocked by the
// per-account sign-in limit.
var ErrTooManyAttempts = errors.New("too many failed sign-in attempts")

// RateLimitError carries the end of the per-account block.
type RateLimitError struct {
	Until time.Time
}

func (e *RateLimitError) Error() string        { return ErrTooManyAttempts.Error() }
func (e *RateLimitError) Is(target error) bool { return target == ErrTooManyAttempts }

// Block reasons.
const (
	BlockRateLimit = "rate_limit"
	BlockLockout   = "lockout_policy"
)

// Block is an account block that a failed attempt just started.
type Block struct {
	Reason string
	Key    string // normalized username
	Until  time.Time
}

// AttemptError is a failed credential check that started one or more
// blocks; it matches ErrInvalidCredentials.
type AttemptError struct {
	Blocks []Block
}

func (e *AttemptError) Error() string        { return ErrInvalidCredentials.Error() }
func (e *AttemptError) Is(target error) bool { return target == ErrInvalidCredentials }

// NormalizeUsername is the key the per-account limit counts under.
func NormalizeUsername(u string) string {
	return strings.ToLower(strings.TrimSpace(u))
}

// ClearLockout resets the policy lockout and the per-account limit of a
// user. It reports whether a user row was updated.
func ClearLockout(ctx context.Context, db *sql.DB, userID string) (bool, error) {
	res, err := db.ExecContext(ctx,
		`UPDATE users SET failed_login_attempts = 0, locked_until = NULL,
		                  login_limit_count = 0, login_limit_since = 0, login_limit_until = 0,
		                  updated_at = CURRENT_TIMESTAMP
		  WHERE id = ?`, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// limitSubject returns the user whose normalized name is key, preferring an
// exact match of raw, or "" when there is none.
func (s *Service) limitSubject(ctx context.Context, raw, key string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM users WHERE lower(trim(username)) = ?
		  ORDER BY username = ? DESC LIMIT 1`, key, raw).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// accountBlockedUntil returns the end of an active per-account block, or
// the zero time. userID "" means key names no user.
func (s *Service) accountBlockedUntil(ctx context.Context, userID, key string) (time.Time, error) {
	if userID == "" {
		if ok, retry := s.unknownLimit.Check(key); !ok {
			return time.Now().Add(retry), nil
		}
		return time.Time{}, nil
	}
	var until int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT login_limit_until FROM users WHERE id = ?`, userID).Scan(&until); err != nil {
		return time.Time{}, err
	}
	if until > time.Now().Unix() {
		return time.Unix(until, 0), nil
	}
	return time.Time{}, nil
}

// failAccount counts one failure against the per-account limit and returns
// the block's end when this failure started one.
func (s *Service) failAccount(ctx context.Context, userID, key string) (time.Time, bool) {
	if userID == "" {
		return s.unknownLimit.Fail(key)
	}
	now := time.Now().Unix()
	window := int64(AccountLimitWindow / time.Second)
	var count int
	err := s.db.QueryRowContext(ctx,
		`UPDATE users SET
		   login_limit_count = CASE WHEN ? - login_limit_since >= ? THEN 1 ELSE login_limit_count + 1 END,
		   login_limit_since = CASE WHEN ? - login_limit_since >= ? THEN ? ELSE login_limit_since END
		 WHERE id = ? RETURNING login_limit_count`,
		now, window, now, window, now, userID).Scan(&count)
	if err != nil {
		slog.Warn("auth: count failed sign-in", "user", userID, "err", err)
		return time.Time{}, false
	}
	if count < AccountLimitMax {
		return time.Time{}, false
	}
	until := now + int64(AccountLimitBlock/time.Second)
	// Conditional so concurrent failures start the block only once.
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET login_limit_until = ? WHERE id = ? AND login_limit_until <= ?`,
		until, userID, now)
	if err != nil {
		slog.Warn("auth: block account", "user", userID, "err", err)
		return time.Time{}, false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return time.Time{}, false
	}
	return time.Unix(until, 0), true
}

// failedAttempt counts a failure against the per-account limit and returns
// the error for it; lockout is a policy lockout the same failure started.
func (s *Service) failedAttempt(ctx context.Context, userID, key string, lockout *Block) error {
	var blocks []Block
	if lockout != nil {
		blocks = append(blocks, *lockout)
	}
	if until, ok := s.failAccount(ctx, userID, key); ok {
		blocks = append(blocks, Block{Reason: BlockRateLimit, Key: key, Until: until})
	}
	if len(blocks) == 0 {
		return ErrInvalidCredentials
	}
	return &AttemptError{Blocks: blocks}
}
