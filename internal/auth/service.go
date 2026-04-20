package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
)

type User struct {
	ID         string   `json:"id"`
	Username   string   `json:"username"`
	Email      string   `json:"email,omitempty"`
	Role       string   `json:"role"`
	ScopeTags  []string `json:"scope_tags,omitempty"` // P.11.3: empty = all hosts
	MFAEnabled bool     `json:"mfa_enabled"`
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
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	settings   SettingsReader // optional; nil = skip policy checks
}

func NewService(db *sql.DB, secret []byte) *Service {
	return &Service{
		db:         db,
		secret:     secret,
		accessTTL:  15 * time.Minute,
		refreshTTL: 30 * 24 * time.Hour,
	}
}

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

// CreateSSOUser inserts a user without a local password. The password
// column holds a random unguessable value so local login can never
// succeed for SSO-only accounts.
func (s *Service) CreateSSOUser(ctx context.Context, username, email, role, provider, subject string) (*User, error) {
	randomPw, err := generatePassword(40)
	if err != nil {
		return nil, err
	}
	hash, err := HashPassword(randomPw)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	var emailNullable any
	if email != "" {
		emailNullable = email
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO users (id, username, email, password, role, oidc_provider, oidc_subject)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, username, emailNullable, hash, role, provider, subject)
	if err != nil {
		return nil, fmt.Errorf("insert sso user: %w", err)
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
	var email, scope sql.NullString
	var totpVerified int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, role, scope_tags, totp_verified FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &email, &u.Role, &scope, &totpVerified)
	if err != nil {
		return nil, err
	}
	if email.Valid {
		u.Email = email.String
	}
	u.ScopeTags = parseScopeTags(scope)
	u.MFAEnabled = totpVerified == 1
	return &u, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, email, role, scope_tags, totp_verified FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var email, scope sql.NullString
		var totp int
		if err := rows.Scan(&u.ID, &u.Username, &email, &u.Role, &scope, &totp); err != nil {
			return nil, err
		}
		if email.Valid {
			u.Email = email.String
		}
		u.ScopeTags = parseScopeTags(scope)
		u.MFAEnabled = totp == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser edits email + role. Scope changes go through
// UpdateUserScope so callers don't need to pass scope on every role
// change.
func (s *Service) UpdateUser(ctx context.Context, id, email, role string) (*User, error) {
	var emailNullable any
	if email != "" {
		emailNullable = email
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET email = ?, role = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		emailNullable, role, id)
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

// Unlock clears the per-user lockout state. Admin-only — called from
// the Users settings page when an operator is locked out after too
// many bad passwords.
func (s *Service) Unlock(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET failed_login_attempts = 0, locked_until = NULL,
		                  updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// ErrAccountLocked is returned by Login when the account is within
// its lockout window. Handlers should surface this as HTTP 423 Locked
// so the UI can show a distinct message from "bad password".
var ErrAccountLocked = errors.New("account locked after too many failed attempts")

// recordFailedLogin bumps the counter and — if the threshold is hit —
// sets locked_until to (now + LockoutDurationMins). When the lockout
// config is zero, it still increments the counter (harmless) but
// never sets locked_until.
func (s *Service) recordFailedLogin(ctx context.Context, userID string, newCount int) {
	policy := s.policy()
	if policy.LockoutMaxAttempts <= 0 || newCount < policy.LockoutMaxAttempts {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE users SET failed_login_attempts = ? WHERE id = ?`,
			newCount, userID)
		return
	}
	lockUntil := time.Now().Add(time.Duration(policy.LockoutDurationMins) * time.Minute)
	_, _ = s.db.ExecContext(ctx,
		`UPDATE users SET failed_login_attempts = ?, locked_until = ? WHERE id = ?`,
		newCount, lockUntil, userID)
}

func (s *Service) Login(ctx context.Context, username, password, userAgent, ip string) (*LoginResult, error) {
	var u User
	var email, scope sql.NullString
	var hash string
	var totpVerified int
	var failedAttempts int
	var lockedUntil sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, role, scope_tags, password, totp_verified,
		        failed_login_attempts, locked_until
		   FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &email, &u.Role, &scope, &hash, &totpVerified,
			&failedAttempts, &lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	// Per-user lockout: if the lockout window is active, refuse fast
	// without verifying the password (so a correct-password-too-late
	// attempt still gets rejected — matches what every competent auth
	// system does).
	if lockedUntil.Valid && time.Now().Before(lockedUntil.Time) {
		return nil, ErrAccountLocked
	}
	if email.Valid {
		u.Email = email.String
	}
	u.ScopeTags = parseScopeTags(scope)
	ok, err := VerifyPassword(password, hash)
	if err != nil || !ok {
		// Record the failure and trip the lockout if we hit the
		// threshold. Errors on the increment itself are logged but
		// don't block the 401 response.
		s.recordFailedLogin(ctx, u.ID, failedAttempts+1)
		return nil, ErrInvalidCredentials
	}
	// Success — clear any accumulated failures.
	if failedAttempts > 0 || lockedUntil.Valid {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE users SET failed_login_attempts = 0, locked_until = NULL WHERE id = ?`,
			u.ID)
	}
	if totpVerified == 1 {
		// Password OK but MFA required — return a pending token.
		token, err := s.issueMFAPending(u.ID)
		if err != nil {
			return nil, err
		}
		return &LoginResult{MFARequired: true, MFAToken: token}, nil
	}
	return s.startSession(ctx, u, userAgent, ip)
}

// VerifyLoginMFA consumes a pending MFA token plus a TOTP (or recovery)
// code and, on success, issues the real session.
func (s *Service) VerifyLoginMFA(ctx context.Context, mfaToken, code, userAgent, ip string) (*LoginResult, error) {
	claims, err := s.parseMFAPending(mfaToken)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	ok, err := s.verifyMFACode(ctx, claims.UserID, code)
	if err != nil || !ok {
		return nil, ErrInvalidCredentials
	}
	u, err := s.GetUser(ctx, claims.UserID)
	if err != nil {
		return nil, err
	}
	return s.startSession(ctx, *u, userAgent, ip)
}

// startSession inserts the sessions row and mints the token pair. Shared
// between password-only login and MFA-completed login.
func (s *Service) startSession(ctx context.Context, u User, userAgent, ip string) (*LoginResult, error) {
	familyID := uuid.NewString()
	expiresAt := time.Now().Add(s.refreshTTL)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (family_id, user_id, current_seq, user_agent, ip, expires_at) VALUES (?, ?, 0, ?, ?, ?)`,
		familyID, u.ID, nullable(userAgent), nullable(ip), expiresAt); err != nil {
		return nil, err
	}
	return s.mintPair(u, familyID, 0, expiresAt)
}

func (s *Service) issueMFAPending(userID string) (string, error) {
	c := Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "dockmesh",
			Subject:   "mfa-pending",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.secret)
}

func (s *Service) parseMFAPending(token string) (*Claims, error) {
	t, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := t.Claims.(*Claims)
	if !ok || !t.Valid || c.Subject != "mfa-pending" {
		return nil, errors.New("invalid mfa token")
	}
	return c, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (*LoginResult, error) {
	claims, err := s.parseRefresh(refreshToken)
	if err != nil {
		return nil, ErrInvalidToken
	}
	var (
		userID     string
		currentSeq int
		revokedAt  sql.NullTime
		expiresAt  time.Time
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT user_id, current_seq, revoked_at, expires_at FROM sessions WHERE family_id = ?`,
		claims.FamilyID).Scan(&userID, &currentSeq, &revokedAt, &expiresAt)
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
	if claims.Seq != currentSeq {
		// Reuse of an older refresh token → revoke the whole family.
		_, _ = s.db.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = CURRENT_TIMESTAMP WHERE family_id = ?`, claims.FamilyID)
		return nil, ErrTokenReused
	}
	newSeq := currentSeq + 1
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET current_seq = ? WHERE family_id = ?`, newSeq, claims.FamilyID); err != nil {
		return nil, err
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.mintPair(*u, claims.FamilyID, newSeq, expiresAt)
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

// Validate parses an access token and returns (userID, role).
func (s *Service) Validate(token string) (string, string, []string, error) {
	c, err := ParseAccessToken(s.secret, token)
	if err != nil {
		return "", "", nil, err
	}
	return c.UserID, c.Role, c.ScopeTags, nil
}

type refreshClaims struct {
	FamilyID string `json:"fam"`
	Seq      int    `json:"seq"`
	jwt.RegisteredClaims
}

func (s *Service) mintPair(u User, familyID string, seq int, expiresAt time.Time) (*LoginResult, error) {
	access, err := IssueAccessToken(s.secret, u.ID, u.Role, u.ScopeTags)
	if err != nil {
		return nil, err
	}
	refresh, err := s.issueRefresh(familyID, seq, expiresAt)
	if err != nil {
		return nil, err
	}
	return &LoginResult{AccessToken: access, RefreshToken: refresh, User: &u}, nil
}

func (s *Service) issueRefresh(familyID string, seq int, expiresAt time.Time) (string, error) {
	c := refreshClaims{
		FamilyID: familyID,
		Seq:      seq,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "dockmesh",
			Subject:   "refresh",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.secret)
}

func (s *Service) parseRefresh(token string) (*refreshClaims, error) {
	t, err := jwt.ParseWithClaims(token, &refreshClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	c, ok := t.Claims.(*refreshClaims)
	if !ok || !t.Valid || c.Subject != "refresh" {
		return nil, errors.New("invalid refresh token")
	}
	return c, nil
}

// IssueWSTicket creates a short-lived (30s) JWT ticket for WebSocket auth (§15.8).
// The client obtains it via POST /api/v1/ws/ticket with a valid Bearer token,
// then passes it as ?ticket=<JWT> on the WebSocket upgrade URL.
func (s *Service) IssueWSTicket(userID, role string) (string, error) {
	c := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * time.Second)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "dockmesh",
			Subject:   "ws-ticket",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.secret)
}

// ValidateWSTicket verifies a WebSocket ticket JWT and returns the user ID.
func (s *Service) ValidateWSTicket(token string) (string, error) {
	c, err := ParseAccessToken(s.secret, token)
	if err != nil {
		return "", err
	}
	return c.UserID, nil
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
