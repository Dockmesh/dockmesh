package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/ratelimit"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidToken       = errors.New("invalid token")
	ErrTokenReused        = errors.New("refresh token reused")
	ErrUserExists         = errors.New("user already exists")
	ErrUsernameTaken      = errors.New("username already in use")
	ErrEmailTaken         = errors.New("email already in use")

	// ErrSessionOutdated marks a refresh token from before per-kind token
	// keys; the user has to sign in again and the UI says why.
	ErrSessionOutdated = errors.New("session predates the current token format")
)

type User struct {
	ID         string   `json:"id"`
	Username   string   `json:"username"`
	DisplayName string  `json:"display_name,omitempty"`
	Email      string   `json:"email,omitempty"`
	Role       string   `json:"role"`
	ScopeTags  []string `json:"scope_tags,omitempty"` // empty = all hosts
	MFAEnabled bool     `json:"mfa_enabled"`
	// Profile timestamps surfaced in the Account page.
	LastLoginAt       *time.Time `json:"last_login_at,omitempty"`
	PasswordChangedAt *time.Time `json:"password_changed_at,omitempty"`
	// LockedUntil is set by ListUsers while the account is blocked by the
	// policy lockout or the per-account login limit.
	LockedUntil *time.Time `json:"locked_until,omitempty"`
	// HasAvatar indicates whether the user has uploaded an avatar.
	// The actual bytes are served from a separate endpoint
	// (GET /users/{id}/avatar) so we don't bloat every list response.
	HasAvatar bool `json:"has_avatar,omitempty"`
	// Effective v2.1 permissions + role scopes. Populated by the
	// /login and /me handlers via rbac.Store lookup; left empty when
	// the User flows through DB-only paths (e.g. ListUsers). Frontend
	// reads these to drive UI gating without replicating the role-perm
	// map locally.
	Permissions []string  `json:"permissions,omitempty"`
	Scopes      *MeScopes `json:"scopes,omitempty"`
}

// MeScopes is the per-scope-type breakdown returned with the User.
// Each list is the set of allowed values for that scope type; empty
// list = unscoped (full access to that resource type).
type MeScopes struct {
	Stacks   []string `json:"stacks"`
	Hosts    []string `json:"hosts"`
	HostTags []string `json:"host_tags"`
}

type LoginResult struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	User         *User  `json:"user,omitempty"`
	// When MFA is required, the backend returns MFARequired=true plus a
	// short-lived MFAToken. The client then calls /auth/mfa with the
	// MFAToken and the 6-digit code to obtain the full session.
	MFARequired bool   `json:"mfa_required,omitempty"`
	MFAToken    string `json:"mfa_token,omitempty"`
}

type Service struct {
	db         *sql.DB
	keys       *TokenKeys
	tickets    *ticketStore
	refreshTTL time.Duration
	settings   SettingsReader // optional; nil = skip policy checks
	// unknownLimit is the per-account sign-in limit for names that match
	// no user.
	unknownLimit *ratelimit.Limiter
	// lockoutMu serializes policy-lockout updates so a lockout starts once.
	lockoutMu sync.Mutex
	// afterAcquire runs once an attempt is counted; tests use it to hang up
	// mid-request.
	afterAcquire func()
}

// NewService panics if secret is shorter than 32 bytes; config always
// generates 32.
func NewService(db *sql.DB, secret []byte) *Service {
	keys, err := NewTokenKeys(secret)
	if err != nil {
		panic(fmt.Sprintf("auth: %v", err))
	}
	return &Service{
		db:           db,
		keys:         keys,
		tickets:      newTicketStore(),
		refreshTTL:   30 * 24 * time.Hour,
		unknownLimit: newUnknownNameLimiter(),
	}
}

// TokenKeys exposes the per-kind signing keys for tokens minted outside
// this package (SSO state cookies).
func (s *Service) TokenKeys() *TokenKeys { return s.keys }

// SetSettings wires the settings store for password-policy + lockout
// lookups. Nil is fine — policy checks default to "no policy".
func (s *Service) SetSettings(r SettingsReader) { s.settings = r }

// policy returns the current policy or zero-value when no settings
// store is attached.
func (s *Service) policy() PolicyConfig {
	if s.settings == nil {
		return PolicyConfig{MinLength: 8, LockoutMaxAttempts: 5, LockoutDurationMins: 15}
	}
	return LoadPolicy(s.settings)
}

// signInConfig returns the current sign-in flow settings, falling back
// to the default snapshot when no store is attached. Used by login,
// refresh, and the cleanup worker so all three see the same numbers.
func (s *Service) signInConfig() SignInConfig {
	if s.settings == nil {
		return SignInConfig{
			AutoCreateOnSSO:       true,
			SessionIdleTTLMin:     60,
			SessionAbsoluteTTLHr:  24,
			SessionRememberMeDays: 14,
			SessionMaxPerUser:     20,
		}
	}
	return LoadSignInConfig(s.settings)
}

// enforceSessionCap revokes the oldest active sessions for `userID` so
// the count stays at most `cap-1` — leaving room for the one we're
// about to insert. cap=0 means "unlimited" and is a no-op. Ordering is
// by last_seen_at then created_at: a session that's still receiving
// refresh hits should be the last one to die, even if it's an old
// family. SQLite's lack of UPDATE+ORDER+LIMIT forces the sub-select.
func (s *Service) enforceSessionCap(ctx context.Context, userID string, cap int) error {
	if cap <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		   SET revoked_at = CURRENT_TIMESTAMP
		 WHERE family_id IN (
		   SELECT family_id FROM sessions
		    WHERE user_id = ?
		      AND revoked_at IS NULL
		      AND expires_at > CURRENT_TIMESTAMP
		    ORDER BY COALESCE(last_seen_at, created_at) ASC, created_at ASC
		    LIMIT MAX(0, (
		      SELECT COUNT(*) FROM sessions
		       WHERE user_id = ?
		         AND revoked_at IS NULL
		         AND expires_at > CURRENT_TIMESTAMP
		    ) - ? + 1)
		 )`, userID, userID, cap)
	return err
}

// Bootstrap creates an initial admin user if no users exist.
// Returns the generated plaintext password so the caller can log it once.
func (s *Service) Bootstrap(ctx context.Context) (username, password string, created bool, err error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return "", "", false, err
	}
	if count > 0 {
		return "", "", false, nil
	}
	pw, err := generatePassword(20)
	if err != nil {
		return "", "", false, err
	}
	if _, err := s.CreateUser(ctx, "admin", "", pw, "admin"); err != nil {
		return "", "", false, err
	}
	return "admin", pw, true, nil
}

func (s *Service) CreateUser(ctx context.Context, username, email, password, role string) (*User, error) {
	if err := ValidatePassword(s.policy(), password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	var emailNullable any
	if email != "" {
		emailNullable = email
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, email, password, role, password_changed_at)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		id, username, emailNullable, hash, role)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
			return nil, ErrUsernameTaken
		}
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return &User{ID: id, Username: username, Email: email, Role: role}, nil
}

// StartSessionForSSO mints a token pair for an SSO-authenticated user,
// bypassing password + MFA checks. Callers are responsible for having
// actually verified the SSO flow before calling this.
func (s *Service) StartSessionForSSO(ctx context.Context, u User, userAgent, ip string) (*LoginResult, error) {
	return s.startSession(ctx, u, userAgent, ip)
}

// parseScopeTags decodes the stringified JSON array stored in
// users.scope_tags. Nil / empty / NULL all return nil, which means
// "all hosts" semantically. Malformed JSON is logged-worthy but we
// treat it as no-scope rather than failing the whole request.
func parseScopeTags(raw sql.NullString) []string {
	if !raw.Valid || raw.String == "" || raw.String == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw.String), &out); err != nil {
		return nil
	}
	return out
}

func (s *Service) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	var email, scope, displayName, avatarMime sql.NullString
	var totpVerified int
	var lastLoginAt, pwChangedAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, role, scope_tags, totp_verified,
		        display_name, last_login_at, password_changed_at, avatar_mime
		   FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &email, &u.Role, &scope, &totpVerified,
			&displayName, &lastLoginAt, &pwChangedAt, &avatarMime)
	if err != nil {
		return nil, err
	}
	if email.Valid {
		u.Email = email.String
	}
	if displayName.Valid {
		u.DisplayName = displayName.String
	}
	if lastLoginAt.Valid {
		t := lastLoginAt.Time
		u.LastLoginAt = &t
	}
	if pwChangedAt.Valid {
		t := pwChangedAt.Time
		u.PasswordChangedAt = &t
	}
	u.ScopeTags = parseScopeTags(scope)
	u.MFAEnabled = totpVerified == 1
	u.HasAvatar = avatarMime.Valid && avatarMime.String != ""
	return &u, nil
}

// SetAvatar stores the avatar bytes + mime type for a user, replacing
// any existing avatar. ClearAvatar with mime == "" wipes the column
// (used when the user removes their avatar).
func (s *Service) SetAvatar(ctx context.Context, id string, mime string, data []byte) error {
	if mime == "" {
		_, err := s.db.ExecContext(ctx,
			`UPDATE users SET avatar_blob = NULL, avatar_mime = NULL,
			                  updated_at = CURRENT_TIMESTAMP
			   WHERE id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET avatar_blob = ?, avatar_mime = ?,
		                  updated_at = CURRENT_TIMESTAMP
		   WHERE id = ?`,
		data, mime, id)
	return err
}

// GetAvatar returns the avatar bytes + mime type for a user.
// Returns (nil, "", nil) when no avatar is set — callers serve a
// placeholder or 404 depending on context.
func (s *Service) GetAvatar(ctx context.Context, id string) ([]byte, string, error) {
	var blob []byte
	var mime sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT avatar_blob, avatar_mime FROM users WHERE id = ?`, id).
		Scan(&blob, &mime)
	if err != nil {
		return nil, "", err
	}
	if !mime.Valid || mime.String == "" {
		return nil, "", nil
	}
	return blob, mime.String, nil
}

// ListActiveUserIDs returns just the ids of currently-known users.
// Used by notifications fan-out so the producer doesn't pay for the
// full User struct + scope-tag parsing on every emit. "Active" here
// means "row exists in the users table" — we don't have a suspended-
// account concept yet, so the filter is degenerate. If/when we add
// one (last_login_at-based or explicit), narrow this query.
func (s *Service) ListActiveUserIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UserAccess returns the role and host-tag scope of one user, for
// deciding who a notification may reach.
func (s *Service) UserAccess(ctx context.Context, userID string) (string, []string, error) {
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	return u.Role, u.ScopeTags, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, email, role, scope_tags, totp_verified,
		        display_name, last_login_at, password_changed_at, avatar_mime,
		        locked_until, login_limit_until
		   FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	now := time.Now()
	for rows.Next() {
		var u User
		var email, scope, displayName, avatarMime sql.NullString
		var totp int
		var lastLoginAt, pwChangedAt, lockedUntil sql.NullTime
		var limitUntil int64
		if err := rows.Scan(&u.ID, &u.Username, &email, &u.Role, &scope, &totp,
			&displayName, &lastLoginAt, &pwChangedAt, &avatarMime,
			&lockedUntil, &limitUntil); err != nil {
			return nil, err
		}
		u.LockedUntil = activeLockEnd(now, lockedUntil, limitUntil)
		if email.Valid {
			u.Email = email.String
		}
		if displayName.Valid {
			u.DisplayName = displayName.String
		}
		if lastLoginAt.Valid {
			t := lastLoginAt.Time
			u.LastLoginAt = &t
		}
		if pwChangedAt.Valid {
			t := pwChangedAt.Time
			u.PasswordChangedAt = &t
		}
		u.ScopeTags = parseScopeTags(scope)
		u.MFAEnabled = totp == 1
		u.HasAvatar = avatarMime.Valid && avatarMime.String != ""
		out = append(out, u)
	}
	return out, rows.Err()
}

// activeLockEnd returns the later of the two block ends that are still in
// the future, or nil when the account is not blocked.
func activeLockEnd(now time.Time, lockedUntil sql.NullTime, limitUntil int64) *time.Time {
	var end time.Time
	if lockedUntil.Valid && lockedUntil.Time.After(now) {
		end = lockedUntil.Time
	}
	if t := time.Unix(limitUntil, 0); limitUntil > 0 && t.After(now) && t.After(end) {
		end = t
	}
	if end.IsZero() {
		return nil
	}
	return &end
}

// UpdateUser edits email, role, and (optionally) display name. Scope
// changes go through UpdateUserScope so callers don't need to pass
// scope on every role change. A role changed here counts as set by an
// admin, so SSO logins no longer overwrite it.
func (s *Service) UpdateUser(ctx context.Context, id, email, role, displayName string) (*User, error) {
	var emailNullable any
	if email != "" {
		emailNullable = email
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET email = ?, display_name = ?,
		                  role_source = CASE WHEN role = ? THEN role_source ELSE ? END,
		                  role = ?, updated_at = CURRENT_TIMESTAMP
		   WHERE id = ?`,
		emailNullable, displayName, role, RoleSourceManual, role, id)
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// UpdateUserScope sets the user's scope_tags. Pass nil / empty slice
// to clear scope (= access to all hosts).
func (s *Service) UpdateUserScope(ctx context.Context, id string, scopeTags []string) (*User, error) {
	var val any
	if len(scopeTags) > 0 {
		b, err := json.Marshal(scopeTags)
		if err != nil {
			return nil, err
		}
		val = string(b)
	}
	// val = nil → stores NULL
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET scope_tags = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		val, id)
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

func (s *Service) DeleteUser(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) ChangePassword(ctx context.Context, id, newPassword string) error {
	if err := ValidatePassword(s.policy(), newPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET password = ?, password_changed_at = CURRENT_TIMESTAMP,
		                  updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		hash, id)
	return err
}

// VerifyUserPassword checks if the supplied plaintext matches the
// stored hash for the given user id. Used by self-password-change to
// require the caller prove they know the current password before
// accepting a new one, so a stolen access token alone can't take
// over the account.
func (s *Service) VerifyUserPassword(ctx context.Context, id, password string) (bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password FROM users WHERE id = ?`, id).Scan(&hash)
	if err != nil {
		return false, err
	}
	return VerifyPassword(password, hash)
}

// Unlock clears the per-user lockout and sign-in limit. Admin-only —
// called from the Users settings page when an operator is locked out after
// too many bad passwords.
func (s *Service) Unlock(ctx context.Context, id string) error {
	_, err := ClearLockout(ctx, s.db, id)
	return err
}

// ErrAccountLocked is returned by Login when the account is within
// its lockout window. Handlers should surface this as HTTP 423 Locked
// so the UI can show a distinct message from "bad password".
var ErrAccountLocked = errors.New("account locked after too many failed attempts")

// LockoutError carries the unlock timestamp alongside the sentinel so
// the HTTP layer can tell the user how long to wait instead of telling
// them to contact an admin. This matters most for homelab + single-admin
// installs where there IS no second admin to unlock you — the lockout
// is auto-expiring by design (see policy.LockoutDurationMins), the
// previous message was just scaring people.
type LockoutError struct {
	Until time.Time
}

func (e *LockoutError) Error() string { return ErrAccountLocked.Error() }
func (e *LockoutError) Is(target error) bool {
	return target == ErrAccountLocked
}

// recordFailedLogin bumps the counter and — if the threshold is hit —
// sets locked_until to (now + LockoutDurationMins) and returns that lockout.
// When the lockout config is zero, it never sets locked_until.
func (s *Service) recordFailedLogin(ctx context.Context, userID, key string) *Block {
	s.lockoutMu.Lock()
	defer s.lockoutMu.Unlock()
	var count int
	if err := s.db.QueryRowContext(ctx,
		`UPDATE users SET failed_login_attempts = failed_login_attempts + 1
		  WHERE id = ? RETURNING failed_login_attempts`, userID).Scan(&count); err != nil {
		slog.Warn("auth: count failed login", "user", userID, "err", err)
		return nil
	}
	policy := s.policy()
	if policy.LockoutMaxAttempts <= 0 || count < policy.LockoutMaxAttempts {
		return nil
	}
	// A request that passed the lockout check before the lock was set can
	// fail after it; that must not restart the lock.
	var lockedUntil sql.NullTime
	if err := s.db.QueryRowContext(ctx,
		`SELECT locked_until FROM users WHERE id = ?`, userID).Scan(&lockedUntil); err != nil {
		return nil
	}
	if lockedUntil.Valid && time.Now().Before(lockedUntil.Time) {
		return nil
	}
	lockUntil := time.Now().Add(time.Duration(policy.LockoutDurationMins) * time.Minute)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET locked_until = ? WHERE id = ?`, lockUntil, userID); err != nil {
		return nil
	}
	return &Block{Reason: BlockLockout, Key: key, Until: lockUntil}
}

// clearFailures resets the lockout counters after a completed sign-in.
func (s *Service) clearFailures(ctx context.Context, userID string) {
	_, _ = s.db.ExecContext(ctx,
		`UPDATE users SET failed_login_attempts = 0, locked_until = NULL,
		                  login_limit_count = 0, login_limit_since = 0, login_limit_until = 0
		  WHERE id = ?`, userID)
}

func (s *Service) Login(ctx context.Context, username, password, userAgent, ip string) (*LoginResult, error) {
	key := NormalizeUsername(username)
	subject, err := s.limitSubject(ctx, username, key)
	if err != nil {
		return nil, err
	}
	var u User
	var email, scope, displayName sql.NullString
	var hash string
	var totpVerified int
	var lockedUntil, pwChangedAt sql.NullTime
	err = s.db.QueryRowContext(ctx,
		`SELECT id, username, email, role, scope_tags, password, totp_verified,
		        locked_until, display_name, password_changed_at
		   FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &email, &u.Role, &scope, &hash, &totpVerified,
			&lockedUntil, &displayName, &pwChangedAt)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Per-user lockout: if the lockout window is active, refuse fast
	// without verifying the password (so a correct-password-too-late
	// attempt still gets rejected — matches what every competent auth
	// system does).
	if found && lockedUntil.Valid && time.Now().Before(lockedUntil.Time) {
		return nil, &LockoutError{Until: lockedUntil.Time}
	}
	att, ok, until, err := s.acquireAccount(ctx, subject, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &RateLimitError{Until: until}
	}
	// The attempt is counted; a client that hangs up must not keep the
	// outcome from being recorded.
	ctx = context.WithoutCancel(ctx)
	if s.afterAcquire != nil {
		s.afterAcquire()
	}
	if !found {
		// Same argon2 work as a wrong password, so response time doesn't
		// tell which usernames exist.
		_, _ = VerifyPassword(password, dummyHash())
		return nil, s.failedAttempt(ctx, att, nil)
	}
	if email.Valid {
		u.Email = email.String
	}
	if displayName.Valid {
		u.DisplayName = displayName.String
	}
	if pwChangedAt.Valid {
		t := pwChangedAt.Time
		u.PasswordChangedAt = &t
	}
	u.ScopeTags = parseScopeTags(scope)
	ok, err = VerifyPassword(password, hash)
	if err != nil || !ok {
		lockout := s.recordFailedLogin(ctx, u.ID, key)
		return nil, s.failedAttempt(ctx, att, lockout)
	}
	if totpVerified == 1 {
		// Password OK but MFA required — return a pending token. The
		// earlier failures stay counted until the second step succeeds,
		// so wrong codes add up with wrong passwords.
		s.refundAccount(ctx, att)
		token, err := s.issueMFAPending(u.ID)
		if err != nil {
			return nil, err
		}
		return &LoginResult{MFARequired: true, MFAToken: token}, nil
	}
	s.clearFailures(ctx, u.ID)
	return s.startSession(ctx, u, userAgent, ip)
}

// VerifyLoginMFA consumes a pending MFA token plus a TOTP (or recovery)
// code and, on success, issues the real session.
func (s *Service) VerifyLoginMFA(ctx context.Context, mfaToken, code, userAgent, ip string) (*LoginResult, error) {
	claims, err := s.parseMFAPending(mfaToken)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	var username string
	var lockedUntil sql.NullTime
	err = s.db.QueryRowContext(ctx,
		`SELECT username, locked_until FROM users WHERE id = ?`, claims.UserID).
		Scan(&username, &lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if lockedUntil.Valid && time.Now().Before(lockedUntil.Time) {
		return nil, &LockoutError{Until: lockedUntil.Time}
	}
	key := NormalizeUsername(username)
	att, ok, until, err := s.acquireAccount(ctx, claims.UserID, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &RateLimitError{Until: until}
	}
	ctx = context.WithoutCancel(ctx)
	if s.afterAcquire != nil {
		s.afterAcquire()
	}
	ok, err = s.verifyMFACode(ctx, claims.UserID, code)
	if err != nil || !ok {
		lockout := s.recordFailedLogin(ctx, claims.UserID, key)
		return nil, s.failedAttempt(ctx, att, lockout)
	}
	s.clearFailures(ctx, claims.UserID)
	u, err := s.GetUser(ctx, claims.UserID)
	if err != nil {
		return nil, err
	}
	return s.startSession(ctx, *u, userAgent, ip)
}

// startSession inserts the sessions row and mints the token pair. Shared
// between password-only login and MFA-completed login.
//
// Session lifetime + concurrency are read from the SignInConfig settings
// (with the hardcoded refreshTTL as a fallback when no settings store is
// wired). Before inserting, sessions over the per-user cap are revoked
// so a runaway client (lost localStorage, repeated re-logins) can never
// pile up indefinitely — the oldest by last_seen_at gets evicted first.
func (s *Service) startSession(ctx context.Context, u User, userAgent, ip string) (*LoginResult, error) {
	cfg := s.signInConfig()
	absoluteTTL := s.refreshTTL
	if cfg.SessionAbsoluteTTLHr > 0 {
		absoluteTTL = time.Duration(cfg.SessionAbsoluteTTLHr) * time.Hour
	}
	if err := s.enforceSessionCap(ctx, u.ID, cfg.SessionMaxPerUser); err != nil {
		// Soft-fail: a cap-enforcement error shouldn't block a legitimate
		// login. Log via the error path and let the new session through.
		_ = err
	}
	familyID := uuid.NewString()
	expiresAt := time.Now().Add(absoluteTTL)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (family_id, user_id, current_seq, user_agent, ip, expires_at) VALUES (?, ?, 0, ?, ?, ?)`,
		familyID, u.ID, nullable(userAgent), nullable(ip), expiresAt); err != nil {
		return nil, err
	}
	// Stamp last_login_at on the user row so the Account header can
	// surface it without the brittle sessions-lookup workaround. Best-
	// effort: a failure here doesn't fail the login.
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = ? WHERE id = ?`, now, u.ID); err == nil {
		u.LastLoginAt = &now
	}
	return s.mintPair(u, familyID, 0, expiresAt)
}

func (s *Service) issueMFAPending(userID string) (string, error) {
	return s.keys.Sign(KindMFAPending, 5*time.Minute, &Claims{UserID: userID})
}

func (s *Service) parseMFAPending(token string) (*Claims, error) {
	c := &Claims{}
	if err := s.keys.Parse(KindMFAPending, token, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (*LoginResult, error) {
	claims, err := s.parseRefresh(refreshToken)
	if err != nil {
		if isPreUpgradeRefresh(refreshToken) {
			return nil, ErrSessionOutdated
		}
		return nil, ErrInvalidToken
	}
	var (
		userID     string
		currentSeq int
		revokedAt  sql.NullTime
		expiresAt  time.Time
		lastSeenAt sql.NullTime
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT user_id, current_seq, revoked_at, expires_at, last_seen_at FROM sessions WHERE family_id = ?`,
		claims.FamilyID).Scan(&userID, &currentSeq, &revokedAt, &expiresAt, &lastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	if revokedAt.Valid {
		return nil, ErrInvalidToken
	}
	if time.Now().After(expiresAt) {
		return nil, ErrInvalidToken
	}
	// Idle timeout: if the session hasn't seen a refresh hit within the
	// configured window, revoke it now and force a fresh login. Catches
	// the "browser tab left open for a week, then resumed" case without
	// waiting for the absolute TTL to run out. 0 = disabled.
	cfg := s.signInConfig()
	if cfg.SessionIdleTTLMin > 0 {
		anchor := expiresAt.Add(-time.Duration(cfg.SessionAbsoluteTTLHr) * time.Hour)
		if lastSeenAt.Valid {
			anchor = lastSeenAt.Time
		}
		idleCutoff := time.Now().Add(-time.Duration(cfg.SessionIdleTTLMin) * time.Minute)
		if anchor.Before(idleCutoff) {
			_, _ = s.db.ExecContext(ctx,
				`UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP WHERE family_id = ?`, claims.FamilyID)
			return nil, ErrInvalidToken
		}
	}
	if claims.Seq != currentSeq {
		// Reuse of an older refresh token → revoke the whole family.
		_, _ = s.db.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP WHERE family_id = ?`, claims.FamilyID)
		return nil, ErrTokenReused
	}
	newSeq := currentSeq + 1
	// Sliding window: bump expires_at on every refresh so an active user
	// never hits the absolute cap unless they go idle long enough to be
	// killed by the idle check above. New deadline = now + absolute TTL.
	newExpires := expiresAt
	if cfg.SessionAbsoluteTTLHr > 0 {
		newExpires = time.Now().Add(time.Duration(cfg.SessionAbsoluteTTLHr) * time.Hour)
	}
	// Conditional on the sequence we read, so of two parallel refreshes with
	// the same token only one rotates the session.
	upd, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET current_seq = ?, expires_at = ? WHERE family_id = ? AND current_seq = ? AND revoked_at IS NULL`,
		newSeq, newExpires, claims.FamilyID, currentSeq)
	if err != nil {
		return nil, err
	}
	if n, err := upd.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, ErrInvalidToken
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.mintPair(*u, claims.FamilyID, newSeq, newExpires)
}

func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	claims, err := s.parseRefresh(refreshToken)
	if err != nil {
		return nil // idempotent
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP WHERE family_id = ? AND revoked_at IS NULL`,
		claims.FamilyID)
	return err
}

// Validate parses an access token and checks that its session is still
// live. Role and scope are re-read from the user row, so a demotion or a
// scope change applies to the next request rather than after the token's
// 15 minutes.
func (s *Service) Validate(ctx context.Context, token string) (*Claims, error) {
	c := &Claims{}
	if err := s.keys.Parse(KindAccess, token, c); err != nil {
		return nil, err
	}
	role, scope, err := s.SessionIdentity(ctx, c.UserID, c.SessionID)
	if err != nil {
		return nil, err
	}
	c.Role, c.ScopeTags = role, scope
	return c, nil
}

// SessionIdentity returns the current role and scope of userID if
// sessionID is one of their live sessions, ErrInvalidToken otherwise.
func (s *Service) SessionIdentity(ctx context.Context, userID, sessionID string) (string, []string, error) {
	if userID == "" || sessionID == "" {
		return "", nil, ErrInvalidToken
	}
	var (
		revokedAt sql.NullTime
		expiresAt time.Time
		role      string
		scope     sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT s.revoked_at, s.expires_at, u.role, u.scope_tags
		  FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.family_id = ? AND s.user_id = ?`, sessionID, userID).
		Scan(&revokedAt, &expiresAt, &role, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrInvalidToken
	}
	if err != nil {
		return "", nil, err
	}
	if revokedAt.Valid || time.Now().After(expiresAt) {
		return "", nil, ErrInvalidToken
	}
	return role, parseScopeTags(scope), nil
}

// RevokeUserSessions ends every live session of userID except
// keepSessionID (pass "" to end all). Access tokens of those sessions stop
// working on their next request because Validate checks the session.
func (s *Service) RevokeUserSessions(ctx context.Context, userID, keepSessionID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP
		 WHERE user_id = ? AND family_id != ? AND revoked_at IS NULL`, userID, keepSessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type refreshClaims struct {
	FamilyID string `json:"fam"`
	Seq      int    `json:"seq"`
	TypedClaims
}

func (s *Service) mintPair(u User, familyID string, seq int, expiresAt time.Time) (*LoginResult, error) {
	access, err := s.keys.Sign(KindAccess, accessTTL, &Claims{UserID: u.ID, SessionID: familyID})
	if err != nil {
		return nil, err
	}
	refresh, err := s.issueRefresh(familyID, seq, expiresAt)
	if err != nil {
		return nil, err
	}
	// Bump the session's last_seen_at so the "active 12m ago" column
	// in the Account / Sessions panel stays meaningful. We do this on
	// the refresh path (every ~15 min) rather than on every API call
	// to keep the write rate sane. Best-effort.
	_, _ = s.db.Exec(`UPDATE sessions SET last_seen_at = CURRENT_TIMESTAMP WHERE family_id = ?`, familyID)
	return &LoginResult{AccessToken: access, RefreshToken: refresh, User: &u}, nil
}

func (s *Service) issueRefresh(familyID string, seq int, expiresAt time.Time) (string, error) {
	c := &refreshClaims{FamilyID: familyID, Seq: seq}
	c.ExpiresAt = jwt.NewNumericDate(expiresAt)
	return s.keys.Sign(KindRefresh, 0, c)
}

func (s *Service) parseRefresh(token string) (*refreshClaims, error) {
	c := &refreshClaims{}
	if err := s.keys.Parse(KindRefresh, token, c); err != nil {
		return nil, err
	}
	return c, nil
}

func generatePassword(n int) (string, error) {
	// Ambiguous characters (0/O, 1/l/I) intentionally excluded.
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
