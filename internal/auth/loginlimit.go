package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/ratelimit"
)

// Per-account sign-in limit: password and two-factor failures for one
// username share it, and it applies whatever the lockout policy says.
// Counters of existing users live on their users row, so every unlock path
// clears them, including `dockmesh admin unlock`, which writes the database
// without going through the server. Names that match no user are counted in
// memory under the same rules, so they answer the same way.
//
// An attempt is counted before the credential is checked and taken back
// when it succeeds, so a burst of parallel requests gets no more than
// AccountLimitMax guesses.
const (
	AccountLimitMax    = 10
	AccountLimitWindow = 15 * time.Minute
	AccountLimitBlock  = 15 * time.Minute

	// unknownNamesMax caps the in-memory counters for names that match no
	// user. When it is reached, further unknown names are refused until
	// counters expire.
	unknownNamesMax = 100_000
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

func newUnknownNameLimiter() *ratelimit.Limiter {
	l := ratelimit.New(AccountLimitMax, AccountLimitWindow, AccountLimitBlock)
	l.MaxKeys = unknownNamesMax
	return l
}

// unknownKey keeps attacker-chosen names out of the limiter's memory.
func unknownKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
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
// exact match of raw, or "" when there is none. The comparison runs in Go
// because SQLite's lower() and trim() only handle ASCII spaces and letters.
func (s *Service) limitSubject(ctx context.Context, raw, key string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username FROM users`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var match string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return "", err
		}
		if name == raw {
			return id, nil
		}
		if match == "" && NormalizeUsername(name) == key {
			match = id
		}
	}
	return match, rows.Err()
}

// acquireAccount counts an attempt against the per-account limit. When the
// account is blocked it returns false and the end of the block. userID ""
// means key names no user.
func (s *Service) acquireAccount(ctx context.Context, userID, key string) (bool, time.Time, error) {
	if userID == "" {
		ok, retry := s.unknownLimit.Acquire(unknownKey(key))
		return ok, time.Now().Add(retry), nil
	}
	now := time.Now().Unix()
	window := int64(AccountLimitWindow / time.Second)
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET
		   login_limit_count = CASE WHEN ? - login_limit_since >= ? THEN 1 ELSE login_limit_count + 1 END,
		   login_limit_since = CASE WHEN ? - login_limit_since >= ? THEN ? ELSE login_limit_since END
		 WHERE id = ? AND login_limit_until <= ?
		   AND (? - login_limit_since >= ? OR login_limit_count < ?)`,
		now, window, now, window, now, userID, now, now, window, AccountLimitMax)
	if err != nil {
		return false, time.Time{}, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, time.Time{}, nil
	}
	var until, since int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT login_limit_until, login_limit_since FROM users WHERE id = ?`, userID).
		Scan(&until, &since); err != nil {
		return false, time.Time{}, err
	}
	if until <= now {
		// All slots are taken by attempts still being checked.
		until = since + window
	}
	return false, time.Unix(until, 0), nil
}

// refundAccount takes back an attempt that succeeded but did not complete
// the sign-in, so only failures stay counted.
func (s *Service) refundAccount(ctx context.Context, userID, key string) {
	if userID == "" {
		s.unknownLimit.Refund(unknownKey(key))
		return
	}
	_, _ = s.db.ExecContext(ctx,
		`UPDATE users SET login_limit_count = login_limit_count - 1
		  WHERE id = ? AND login_limit_count > 0`, userID)
}

// settleAccount records that an acquired attempt failed and returns the
// block's end when this failure started one.
func (s *Service) settleAccount(ctx context.Context, userID, key string) (time.Time, bool) {
	if userID == "" {
		return s.unknownLimit.Settle(unknownKey(key))
	}
	now := time.Now().Unix()
	until := now + int64(AccountLimitBlock/time.Second)
	// Conditional so concurrent failures start the block only once.
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET login_limit_until = ?
		  WHERE id = ? AND login_limit_count >= ? AND login_limit_until <= ?`,
		until, userID, AccountLimitMax, now)
	if err != nil {
		slog.Warn("auth: block account", "user", userID, "err", err)
		return time.Time{}, false
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return time.Time{}, false
	}
	return time.Unix(until, 0), true
}

// failedAttempt settles a failed attempt and returns the error for it;
// lockout is a policy lockout the same failure started.
func (s *Service) failedAttempt(ctx context.Context, userID, key string, lockout *Block) error {
	var blocks []Block
	if lockout != nil {
		blocks = append(blocks, *lockout)
	}
	if until, ok := s.settleAccount(ctx, userID, key); ok {
		blocks = append(blocks, Block{Reason: BlockRateLimit, Key: key, Until: until})
	}
	if len(blocks) == 0 {
		return ErrInvalidCredentials
	}
	return &AttemptError{Blocks: blocks}
}
