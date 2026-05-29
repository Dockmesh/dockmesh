// Package invites issues one-time user-invitation tokens. The flow
// follows Coolify's pattern: admin generates a link, copies it out,
// shares it manually. No SMTP dependency. The recipient hits the
// public accept endpoint with the raw token, picks a username +
// password, and lands as a real user with the role + scope the admin
// chose at invite time.
//
// Tokens are 32 random bytes hex-encoded (64 chars). Only the SHA-256
// hash of the raw token is persisted; the raw value is shown to the
// admin exactly once at create-time. A database leak therefore does
// not hand over still-valid invites.
//
// Tokens expire 24 hours after creation by default and are single-use.
// A background goroutine deletes used + expired rows older than 30
// days so the table doesn't grow forever.
package invites

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

const (
	defaultTTL    = 24 * time.Hour
	cleanupAfter  = 30 * 24 * time.Hour
	cleanupEvery  = 1 * time.Hour
	tokenRandSize = 32
)

var (
	ErrNotFound = errors.New("invite not found")
	ErrExpired  = errors.New("invite expired")
	ErrUsed     = errors.New("invite already used")
)

// Invite is the public row shape. Token (raw) is only ever populated
// in the Create response — never on reads.
type Invite struct {
	ID         int64     `json:"id"`
	Token      string    `json:"token,omitempty"`
	Role       string    `json:"role"`
	ScopeTags  []string  `json:"scope_tags"`
	EmailHint  string    `json:"email_hint,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	AcceptURL  string    `json:"accept_url,omitempty"`
}

// Service persists invites and is also responsible for the cleanup
// goroutine. It does NOT own user creation — the accept handler calls
// into the auth/users service after validating the token.
type Service struct {
	db      *sql.DB
	baseURL string

	stop chan struct{}
}

func New(db *sql.DB, baseURL string) *Service {
	return &Service{db: db, baseURL: baseURL, stop: make(chan struct{})}
}

// Start kicks off the cleanup goroutine. Safe to call once.
func (s *Service) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(cleanupEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-t.C:
				if err := s.cleanup(ctx); err != nil {
					slog.Debug("invite cleanup", "err", err)
				}
			}
		}
	}()
}

func (s *Service) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

// CreateInput captures everything the admin picks at invite time. The
// resulting user inherits role + scope from these values; the
// recipient cannot escalate by tweaking the accept payload.
type CreateInput struct {
	Role      string
	ScopeTags []string
	EmailHint string
	TTL       time.Duration // 0 = defaultTTL
	CreatedBy string
}

// Create generates a fresh token, persists its hash, and returns the
// row with the RAW token populated. Caller renders the accept URL and
// shows it to the admin once.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Invite, error) {
	raw := make([]byte, tokenRandSize)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw)
	hash := sha256Hex(token)

	ttl := in.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	expiresAt := time.Now().Add(ttl)

	tagsJSON, err := json.Marshal(in.ScopeTags)
	if err != nil {
		return nil, err
	}
	if string(tagsJSON) == "null" {
		tagsJSON = []byte("[]")
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO invite_tokens (token_hash, role, scope_tags_json, email_hint, expires_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?)`,
		hash, in.Role, string(tagsJSON), in.EmailHint, expiresAt, in.CreatedBy)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	return &Invite{
		ID:        id,
		Token:     token,
		Role:      in.Role,
		ScopeTags: in.ScopeTags,
		EmailHint: in.EmailHint,
		ExpiresAt: expiresAt,
		CreatedBy: in.CreatedBy,
		CreatedAt: time.Now(),
		AcceptURL: s.acceptURL(token),
	}, nil
}

// Get returns the invite preview for the recipient's accept page.
// Does NOT mark the token used. ErrNotFound when the token doesn't
// match, ErrExpired when expires_at is past, ErrUsed when used_at is
// already set.
func (s *Service) Get(ctx context.Context, rawToken string) (*Invite, error) {
	hash := sha256Hex(rawToken)
	row := s.db.QueryRowContext(ctx, `
		SELECT id, role, scope_tags_json, email_hint, expires_at, used_at, created_by, created_at
		FROM invite_tokens WHERE token_hash = ?`, hash)
	inv := &Invite{}
	var tagsJSON string
	var usedAt sql.NullTime
	if err := row.Scan(&inv.ID, &inv.Role, &tagsJSON, &inv.EmailHint, &inv.ExpiresAt, &usedAt, &inv.CreatedBy, &inv.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(tagsJSON), &inv.ScopeTags)
	if usedAt.Valid {
		t := usedAt.Time
		inv.UsedAt = &t
		return inv, ErrUsed
	}
	if time.Now().After(inv.ExpiresAt) {
		return inv, ErrExpired
	}
	return inv, nil
}

// MarkUsed atomically flags the invite consumed. Called by the accept
// handler AFTER the user row has been successfully created. Returns
// ErrUsed if a race lost (someone else accepted at the same time).
func (s *Service) MarkUsed(ctx context.Context, rawToken string) error {
	hash := sha256Hex(rawToken)
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `
		UPDATE invite_tokens SET used_at = ?
		WHERE token_hash = ? AND used_at IS NULL`, now, hash)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUsed
	}
	return nil
}

// List returns recent invites (admin overview). Includes expired +
// used rows that are still inside the cleanup retention window.
func (s *Service) List(ctx context.Context, limit int) ([]Invite, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, role, scope_tags_json, email_hint, expires_at, used_at, created_by, created_at
		FROM invite_tokens ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var inv Invite
		var tagsJSON string
		var usedAt sql.NullTime
		if err := rows.Scan(&inv.ID, &inv.Role, &tagsJSON, &inv.EmailHint, &inv.ExpiresAt, &usedAt, &inv.CreatedBy, &inv.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tagsJSON), &inv.ScopeTags)
		if usedAt.Valid {
			t := usedAt.Time
			inv.UsedAt = &t
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// Revoke deletes a pending invite by id so a leaked link can be
// invalidated before its expiry.
func (s *Service) Revoke(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM invite_tokens WHERE id = ?`, id)
	return err
}

func (s *Service) cleanup(ctx context.Context) error {
	cutoff := time.Now().Add(-cleanupAfter)
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM invite_tokens WHERE
			(used_at IS NOT NULL AND used_at < ?) OR
			(used_at IS NULL AND expires_at < ?)`,
		cutoff, cutoff)
	return err
}

func (s *Service) acceptURL(token string) string {
	if s.baseURL == "" {
		return "/invite/" + token
	}
	return s.baseURL + "/invite/" + token
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
