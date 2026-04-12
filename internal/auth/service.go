package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidToken       = errors.New("invalid token")
	ErrTokenReused        = errors.New("refresh token reused")
	ErrUserExists         = errors.New("user already exists")
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role"`
}

type LoginResult struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	User         User   `json:"user"`
}

type Service struct {
	db         *sql.DB
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewService(db *sql.DB, secret []byte) *Service {
	return &Service{
		db:         db,
		secret:     secret,
		accessTTL:  15 * time.Minute,
		refreshTTL: 30 * 24 * time.Hour,
	}
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
		`INSERT INTO users (id, username, email, password, role) VALUES (?, ?, ?, ?, ?)`,
		id, username, emailNullable, hash, role)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return &User{ID: id, Username: username, Email: email, Role: role}, nil
}

func (s *Service) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	var email sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, role FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &email, &u.Role)
	if err != nil {
		return nil, err
	}
	if email.Valid {
		u.Email = email.String
	}
	return &u, nil
}

func (s *Service) Login(ctx context.Context, username, password, userAgent, ip string) (*LoginResult, error) {
	var u User
	var email sql.NullString
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, role, password FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &email, &u.Role, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if email.Valid {
		u.Email = email.String
	}
	ok, err := VerifyPassword(password, hash)
	if err != nil || !ok {
		return nil, ErrInvalidCredentials
	}
	familyID := uuid.NewString()
	expiresAt := time.Now().Add(s.refreshTTL)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (family_id, user_id, current_seq, user_agent, ip, expires_at) VALUES (?, ?, 0, ?, ?, ?)`,
		familyID, u.ID, nullable(userAgent), nullable(ip), expiresAt); err != nil {
		return nil, err
	}
	return s.mintPair(u, familyID, 0, expiresAt)
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

// Validate parses an access token and returns the user ID.
func (s *Service) Validate(token string) (string, error) {
	c, err := ParseAccessToken(s.secret, token)
	if err != nil {
		return "", err
	}
	return c.UserID, nil
}

type refreshClaims struct {
	FamilyID string `json:"fam"`
	Seq      int    `json:"seq"`
	jwt.RegisteredClaims
}

func (s *Service) mintPair(u User, familyID string, seq int, expiresAt time.Time) (*LoginResult, error) {
	access, err := IssueAccessToken(s.secret, u.ID)
	if err != nil {
		return nil, err
	}
	refresh, err := s.issueRefresh(familyID, seq, expiresAt)
	if err != nil {
		return nil, err
	}
	return &LoginResult{AccessToken: access, RefreshToken: refresh, User: u}, nil
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
