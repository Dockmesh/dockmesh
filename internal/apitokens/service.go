// Package apitokens implements long-lived bearer tokens for CI/CD,
// scripting, and external integrations. These are distinct from the
// short-lived user JWTs issued by the auth package:
//
//   - They don't auto-expire (unless an expiry is explicitly set)
//   - They carry a pinned role at creation time (not tied to a user session)
//   - They're created through a settings UI and can be revoked without
//     touching the user's session
//
// Token format: "dmt_" + 40 chars of base64url-encoded randomness.
// Server stores only an argon2id hash plus the first 12 chars as a
// display prefix.
package apitokens

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
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
)

// Token is one row in api_tokens. The plaintext value is never stored
// after creation — only token_hash survives.
type Token struct {
	ID         int64      `json:"id"`
	Prefix     string     `json:"prefix"` // 'dmt_XXXXXXXX', shown in UI
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	CreatedBy  *int64     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
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
		&t.ID, &hash, &t.Name, &t.Role, &t.CreatedBy,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &lastIP,
		&t.RevokedAt,
	)
	if err != nil {
		return nil, "", err
	}
	if lastIP.Valid {
		t.LastUsedIP = lastIP.String
	}
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
		&t.ID, &t.Prefix, &t.Name, &t.Role, &t.CreatedBy,
		&t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &lastIP,
		&t.RevokedAt,
	)
	if err != nil {
		return nil, err
	}
	if lastIP.Valid {
		t.LastUsedIP = lastIP.String
	}
	return t, nil
}

// CreateInput carries the fields required to mint a new token.
type CreateInput struct {
	Name            string
	Role            string
	ExpiresInDays   int    // 0 = no expiry
	CreatedByUserID *int64 // nil for CLI-created tokens
}

// Service persists tokens and provides middleware-friendly lookup.
type Service struct {
	db *sql.DB

	// touchBuffer collects last-used updates so we don't issue a write
	// per request. Flushed periodically by the bg goroutine started in
	// Start().
	mu      sync.Mutex
	touches map[int64]touchEntry
}

type touchEntry struct {
	at time.Time
	ip string
}

// New returns a fresh service backed by the given DB.
func New(db *sql.DB) *Service {
	return &Service{
		db:      db,
		touches: make(map[int64]touchEntry),
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

	raw := make([]byte, rawBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("rand: %w", err)
	}
	plaintext = TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	prefix := plaintext[:prefixLen]
	hash, err := auth.HashPassword(plaintext)
	if err != nil {
		return "", nil, fmt.Errorf("hash: %w", err)
	}

	var expiresAt *time.Time
	if in.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
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

	token = &Token{
		ID:        id,
		Prefix:    prefix,
		Name:      in.Name,
		Role:      in.Role,
		CreatedBy: in.CreatedByUserID,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}
	return plaintext, token, nil
}

// Validate looks up a token by its plaintext and returns the metadata
// record if it is active (not revoked, not expired). This is called
// from auth middleware on every request — performance-critical.
//
// Strategy: extract prefix (first 12 chars), find candidate rows by
// prefix index, verify argon2id hash on the candidate. Prefix collision
// is rare (64^8 = 281T space) but possible — iterate if found.
func (s *Service) Validate(ctx context.Context, plaintext string) (*Token, error) {
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		return nil, ErrInvalid
	}
	if len(plaintext) < prefixLen {
		return nil, ErrInvalid
	}
	prefix := plaintext[:prefixLen]

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, token_hash, name, role, created_by_user_id,
		       created_at, expires_at, last_used_at, last_used_ip,
		       revoked_at
		FROM api_tokens
		WHERE token_prefix = ?`,
		prefix,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		t, hash, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		t.Prefix = prefix

		ok, err := auth.VerifyPassword(plaintext, hash)
		if err != nil || !ok {
			continue
		}

		if t.RevokedAt != nil {
			return nil, ErrRevoked
		}
		if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
			return nil, ErrExpired
		}
		return t, nil
	}
	return nil, ErrNotFound
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
		SELECT id, token_prefix, name, role, created_by_user_id,
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
		SELECT id, token_prefix, name, role, created_by_user_id,
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
