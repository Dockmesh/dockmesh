// Package apitokens implements long-lived bearer tokens for CI/CD,
// scripting, and external integrations. These are distinct from the
// short-lived user JWTs issued by the auth package:
//
//   - They don't auto-expire (unless an expiry is explicitly set)
//   - They carry a role chosen at creation and belong to the user who
//     created them; they never grant more than that user currently holds
//   - They're created through a settings UI and can be revoked without
//     touching the user's session
//
// Token format: "dmt_" + 40 chars of base64url-encoded randomness.
// Server stores only a hash plus the first 12 chars as a display prefix.
package apitokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/auth"
)

const (
	// TokenPrefix is the distinguishing marker for API tokens in the
	// Authorization header. Makes them easy to tell apart from user JWTs
	// in logs and middleware.
	TokenPrefix = "dmt_"

	// prefixLen is the number of plaintext chars stored for display:
	// "dmt_" + 8 random chars.
	prefixLen = 12

	// rawBytes is the random byte count generated per token. 30 bytes
	// → 40 base64url chars → ~180 bits of entropy.
	rawBytes = 30
)

// Errors returned by the service.
var (
	ErrNotFound = errors.New("api token not found")
	ErrRevoked  = errors.New("api token revoked")
	ErrExpired  = errors.New("api token expired")
	ErrInvalid  = errors.New("invalid api token")
	// ErrBusy means too many argon2 verifications are already running.
	ErrBusy = errors.New("api token verification busy")
)

// Tokens carry ~180 bits of randomness, so a plain SHA-256 is enough to
// store them. Older tokens were stored as argon2id (64 MiB per check); they
// are rehashed on their first successful use, and argon2 checks run at
// most maxArgon2 at a time so bad requests can't exhaust memory.
const (
	sha256Scheme = "sha256$"
	maxArgon2    = 2
	argon2Wait   = 5 * time.Second
)

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return sha256Scheme + hex.EncodeToString(sum[:])
}

// Token is one row in api_tokens. The plaintext value is never stored
// after creation — only token_hash survives.
type Token struct {
	ID        int64   `json:"id"`
	Prefix    string  `json:"prefix"` // 'dmt_XXXXXXXX', shown in UI
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	CreatedBy *string `json:"created_by,omitempty"`
	// Legacy tokens predate owner tracking: no owner, pinned role.
	Legacy bool `json:"legacy"`
	// OwnerMissing: the owner was deleted, so the token no longer works.
	OwnerMissing bool       `json:"owner_missing,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP   string     `json:"last_used_ip,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

// scanRow reads one row into a Token, using sql.NullString for the
// nullable text column so the sqlite3 driver is happy.
func scanRow(row interface {
	Scan(dest ...any) error
}) (*Token, string, error) {
	t := &Token{}
	var hash string
	var lastIP sql.NullString
	err := row.Scan(
		&t.ID, &hash, &t.Name, &t.Role, &t.CreatedBy, &t.Legacy,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &lastIP,
		&t.RevokedAt,
	)
	if err != nil {
		return nil, "", err
	}
	if lastIP.Valid {
		t.LastUsedIP = lastIP.String
	}
	t.OwnerMissing = !t.Legacy && t.CreatedBy == nil
	return t, hash, nil
}

// scanRowListing is the same as scanRow but for the list/get queries
// that return token_prefix instead of token_hash.
func scanRowListing(row interface {
	Scan(dest ...any) error
}) (*Token, error) {
	t := &Token{}
	var lastIP sql.NullString
	err := row.Scan(
		&t.ID, &t.Prefix, &t.Name, &t.Role, &t.CreatedBy, &t.Legacy,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &lastIP,
		&t.RevokedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastIP.Valid {
		t.LastUsedIP = lastIP.String
	}
	t.OwnerMissing = !t.Legacy && t.CreatedBy == nil
	return t, nil
}

// CreateInput carries the fields required to mint a new token.
type CreateInput struct {
	Name            string
	Role            string
	ExpiresInDays   int        // 0 = no expiry
	NotAfter        *time.Time // caps the expiry (tokens minted by tokens)
	CreatedByUserID string     // owner; required
}

// Service persists tokens and provides middleware-friendly lookup.
type Service struct {
	db *sql.DB

	// touchBuffer collects last-used updates so we don't issue a write
	// per request. Flushed periodically by the bg goroutine started in
	// Start().
	mu      sync.Mutex
	touches map[int64]touchEntry

	argon2Slots chan struct{}
}

type touchEntry struct {
	at time.Time
	ip string
}

// New returns a fresh service backed by the given DB.
func New(db *sql.DB) *Service {
	return &Service{
		db:          db,
		touches:     make(map[int64]touchEntry),
		argon2Slots: make(chan struct{}, maxArgon2),
	}
}

// Start launches the background flusher for last-used updates. Call
// once at server startup. Flushes every 60s and on shutdown.
func (s *Service) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				// Final flush.
				s.flushTouches(context.Background())
				return
			case <-t.C:
				s.flushTouches(ctx)
			}
		}
	}()
}

// Create mints a new token. Returns the plaintext ONCE — the caller
// must show it to the user and discard it. Subsequent reads can only
// see the prefix.
func (s *Service) Create(ctx context.Context, in CreateInput) (plaintext string, token *Token, err error) {
	if in.Name == "" {
		return "", nil, errors.New("name required")
	}
	if in.Role == "" {
		return "", nil, errors.New("role required")
	}
	if in.CreatedByUserID == "" {
		return "", nil, errors.New("owner required")
	}

	raw := make([]byte, rawBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	plaintext = TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	prefix := plaintext[:prefixLen]
	hash := hashToken(plaintext)

	var expiresAt *time.Time
	if in.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}
	if in.NotAfter != nil && (expiresAt == nil || expiresAt.After(*in.NotAfter)) {
		t := *in.NotAfter
		expiresAt = &t
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (token_prefix, token_hash, name, role,
		                        created_by_user_id, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		prefix, hash, in.Name, in.Role, in.CreatedByUserID, expiresAt,
	)
	if err != nil {
		return "", nil, fmt.Errorf("insert: %w", err)
	}
	id, _ := res.LastInsertId()

	owner := in.CreatedByUserID
	token = &Token{
		ID:        id,
		Prefix:    prefix,
		Name:      in.Name,
		Role:      in.Role,
		CreatedBy: &owner,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}
	return plaintext, token, nil
}

// Validate looks up a token by its plaintext and returns the metadata
// record if it is active (not revoked, not expired). Called from the auth
// middleware on every request. token_prefix is unique, so at most one
// hash is checked per request.
func (s *Service) Validate(ctx context.Context, plaintext string) (*Token, error) {
	if !strings.HasPrefix(plaintext, TokenPrefix) || len(plaintext) < prefixLen {
		return nil, ErrInvalid
	}
	prefix := plaintext[:prefixLen]

	t, hash, err := scanRow(s.db.QueryRowContext(ctx, `
		SELECT id, token_hash, name, role, created_by_user_id, legacy,
		       created_at, expires_at, last_used_at, last_used_ip,
		       revoked_at
		FROM api_tokens
		WHERE token_prefix = ?`,
		prefix,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Prefix = prefix

	// Dead tokens are refused before hashing, so their known prefixes can't
	// be used to keep the argon2 slots busy.
	if t.RevokedAt != nil {
		return nil, ErrRevoked
	}
	if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
		return nil, ErrExpired
	}
	ok, err := s.verify(ctx, t.ID, plaintext, hash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return t, nil
}

func (s *Service) verify(ctx context.Context, id int64, plaintext, stored string) (bool, error) {
	if strings.HasPrefix(stored, sha256Scheme) {
		return subtle.ConstantTimeCompare([]byte(hashToken(plaintext)), []byte(stored)) == 1, nil
	}
	timer := time.NewTimer(argon2Wait)
	defer timer.Stop()
	select {
	case s.argon2Slots <- struct{}{}:
	case <-timer.C:
		return false, ErrBusy
	case <-ctx.Done():
		return false, ctx.Err()
	}
	ok, err := auth.VerifyPassword(plaintext, stored)
	<-s.argon2Slots
	if err != nil || !ok {
		return false, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET token_hash = ? WHERE id = ? AND token_hash = ?`,
		hashToken(plaintext), id, stored); err != nil {
		return false, err
	}
	return true, nil
}

// CountActiveLegacy counts legacy tokens that still authenticate.
func (s *Service) CountActiveLegacy(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT expires_at FROM api_tokens WHERE legacy = 1 AND revoked_at IS NULL`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	now := time.Now()
	for rows.Next() {
		var exp *time.Time
		if err := rows.Scan(&exp); err != nil {
			return 0, err
		}
		if exp == nil || exp.After(now) {
			n++
		}
	}
	return n, rows.Err()
}

// TouchAsync records the use of a token. Buffered — writes land in DB
// at most once per minute via flushTouches.
func (s *Service) TouchAsync(id int64, ip string) {
	s.mu.Lock()
	s.touches[id] = touchEntry{at: time.Now(), ip: ip}
	s.mu.Unlock()
}

func (s *Service) flushTouches(ctx context.Context) {
	s.mu.Lock()
	if len(s.touches) == 0 {
		s.mu.Unlock()
		return
	}
	toWrite := s.touches
	s.touches = make(map[int64]touchEntry)
	s.mu.Unlock()

	for id, t := range toWrite {
		_, _ = s.db.ExecContext(ctx, `
			UPDATE api_tokens SET last_used_at = ?, last_used_ip = ?
			WHERE id = ?`,
			t.at, t.ip, id,
		)
	}
}

// List returns all tokens ordered by most-recently-created. Callers
// should filter out sensitive fields before responding to HTTP clients
// (there are none in the struct — token plaintext is never stored).
func (s *Service) List(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, token_prefix, name, role, created_by_user_id, legacy,
		       created_at, expires_at, last_used_at, last_used_ip,
		       revoked_at
		FROM api_tokens
		ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Non-nil slice so the JSON response is [] not null — the UI does
	// .length checks directly on the result.
	out := make([]Token, 0)
	for rows.Next() {
		t, err := scanRowListing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// Get returns a single token by id (without plaintext).
func (s *Service) Get(ctx context.Context, id int64) (*Token, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, token_prefix, name, role, created_by_user_id, legacy,
		       created_at, expires_at, last_used_at, last_used_ip,
		       revoked_at
		FROM api_tokens WHERE id = ?`,
		id,
	)
	t, err := scanRowListing(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// Revoke marks a token as revoked. Subsequent Validate calls return
// ErrRevoked. The row is kept for audit / forensic purposes.
func (s *Service) Revoke(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE api_tokens SET revoked_at = CURRENT_TIMESTAMP
		WHERE id = ? AND revoked_at IS NULL`,
		id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
