// Package registries stores private-image-registry credentials so users
// don't have to re-enter them on every private pull. Passwords are
// age-encrypted at rest via the shared secrets service.
//
// P.11.7. Follow-ups tracked in the Slices doc:
//   - P.12.28: propagate credentials to remote agents over the mTLS link.
//   - P.12.28: auto-apply a configured docker.io entry to unprefixed
//     pulls (lifts Docker Hub rate-limit without a .dockerconfigjson).
package registries

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/registry"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/secrets"
)

var (
	ErrNotFound  = errors.New("registry not found")
	ErrDuplicate = errors.New("a registry with this URL already exists")
)

// Registry is the DB-backed entity. Password is never exposed directly
// through JSON — use PlaintextPassword() from inside the auth path only.
type Registry struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	URL          string     `json:"url"`
	Username     string     `json:"username,omitempty"`
	HasPassword  bool       `json:"has_password"`
	ScopeTags    []string   `json:"scope_tags,omitempty"`
	LastTestedAt *time.Time `json:"last_tested_at,omitempty"`
	LastTestOK   *bool      `json:"last_test_ok,omitempty"`
	LastTestErr  string     `json:"last_test_error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`

	// plaintextPassword is populated only by Resolve() / internal lookups,
	// never by List() or Get(). Zero value otherwise.
	plaintextPassword string
}

// Input is the CRUD payload. Password is optional on update — empty
// means "keep existing". Sending an explicit empty string cannot be
// distinguished from "no change" here; use the dedicated
// ClearPassword flag for that case.
type Input struct {
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Username      string   `json:"username,omitempty"`
	Password      string   `json:"password,omitempty"`
	ClearPassword bool     `json:"clear_password,omitempty"`
	ScopeTags     []string `json:"scope_tags,omitempty"`
}

type Service struct {
	db      *sql.DB
	secrets *secrets.Service
}

func New(db *sql.DB, secretsSvc *secrets.Service) *Service {
	return &Service{db: db, secrets: secretsSvc}
}

// NormalizeURL strips scheme, trailing slashes, and lowercases the host.
// Matches the format `docker login` stores in ~/.docker/config.json so
// two operators writing "https://ghcr.io/" and "ghcr.io" land on the
// same row.
func NormalizeURL(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimRight(s, "/")
	return s
}

// RegistryForImage extracts the registry host from a docker image
// reference. Rules matching docker's canonical parser:
//
//	nginx                   → docker.io
//	foo/bar                 → docker.io
//	ghcr.io/foo/bar         → ghcr.io
//	registry:5000/foo       → registry:5000
//	localhost/foo           → localhost
//
// The first path element is treated as a registry if it contains a
// '.', a ':', or is literally "localhost" — same heuristic the Docker
// reference grammar uses.
func RegistryForImage(ref string) string {
	if i := strings.IndexByte(ref, '/'); i > 0 {
		first := ref[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			return first
		}
	}
	return "docker.io"
}

// -----------------------------------------------------------------------------
// CRUD
// -----------------------------------------------------------------------------

func (s *Service) List(ctx context.Context) ([]Registry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, url, COALESCE(username, ''),
		       password_encrypted IS NOT NULL AS has_password,
		       scope_tags, last_tested_at, last_test_ok, COALESCE(last_test_error, ''),
		       created_at, updated_at
		  FROM registries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Registry{}
	for rows.Next() {
		r, err := scanRegistry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id int64) (*Registry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, url, COALESCE(username, ''),
		       password_encrypted IS NOT NULL AS has_password,
		       scope_tags, last_tested_at, last_test_ok, COALESCE(last_test_error, ''),
		       created_at, updated_at
		  FROM registries WHERE id = ?`, id)
	r, err := scanRegistry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *Service) Create(ctx context.Context, in Input) (*Registry, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}
	url := NormalizeURL(in.URL)

	var encPw []byte
	if in.Password != "" {
		enc, err := s.secrets.Encrypt([]byte(in.Password))
		if err != nil {
			return nil, fmt.Errorf("encrypt: %w", err)
		}
		encPw = enc
	}
	scope := marshalScope(in.ScopeTags)

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO registries (name, url, username, password_encrypted, scope_tags)
		VALUES (?, ?, ?, ?, ?)`,
		in.Name, url, nullable(in.Username), encPw, scope)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

func (s *Service) Update(ctx context.Context, id int64, in Input) (*Registry, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}
	url := NormalizeURL(in.URL)
	scope := marshalScope(in.ScopeTags)

	// Password semantics: explicit clear > new value > keep existing.
	switch {
	case in.ClearPassword:
		if _, err := s.db.ExecContext(ctx, `
			UPDATE registries
			   SET name = ?, url = ?, username = ?, password_encrypted = NULL,
			       scope_tags = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			in.Name, url, nullable(in.Username), scope, id); err != nil {
			return nil, updateErr(err)
		}
	case in.Password != "":
		enc, err := s.secrets.Encrypt([]byte(in.Password))
		if err != nil {
			return nil, fmt.Errorf("encrypt: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE registries
			   SET name = ?, url = ?, username = ?, password_encrypted = ?,
			       scope_tags = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			in.Name, url, nullable(in.Username), enc, scope, id); err != nil {
			return nil, updateErr(err)
		}
	default:
		if _, err := s.db.ExecContext(ctx, `
			UPDATE registries
			   SET name = ?, url = ?, username = ?, scope_tags = ?,
			       updated_at = CURRENT_TIMESTAMP
			 WHERE id = ?`,
			in.Name, url, nullable(in.Username), scope, id); err != nil {
			return nil, updateErr(err)
		}
	}
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM registries WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordTest persists the result of a test-login attempt so the UI can
// show "last verified X minutes ago" without forcing a retest on every
// page load.
func (s *Service) RecordTest(ctx context.Context, id int64, ok bool, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE registries
		   SET last_tested_at = CURRENT_TIMESTAMP,
		       last_test_ok = ?,
		       last_test_error = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, boolInt(ok), nullable(errMsg), id)
	return err
}

// -----------------------------------------------------------------------------
// Auth resolution (used by the image-pull handler)
// -----------------------------------------------------------------------------

// ResolveAuth looks up credentials for an image reference that will be
// pulled against hostTags. Returns the base64-encoded X-Registry-Auth
// blob ready to hand to Docker's ImagePullOptions.RegistryAuth, and the
// registry that provided it (for audit), or ("", nil, nil) when no
// matching entry exists — the caller should then fall back to an
// anonymous pull.
func (s *Service) ResolveAuth(ctx context.Context, image string, hostTags []string) (string, *Registry, error) {
	host := RegistryForImage(image)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, url, COALESCE(username, ''),
		       password_encrypted,
		       scope_tags, last_tested_at, last_test_ok, COALESCE(last_test_error, ''),
		       created_at, updated_at
		  FROM registries WHERE url = ? AND password_encrypted IS NOT NULL`, host)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Registry
		var pw []byte
		var scope sql.NullString
		var lastAt sql.NullTime
		var lastOK sql.NullBool
		var lastErr string
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &r.Username,
			&pw, &scope, &lastAt, &lastOK, &lastErr, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return "", nil, err
		}
		r.ScopeTags = parseScope(scope)
		if !rbac.ScopeMatchesHost(r.ScopeTags, hostTags) {
			continue
		}
		plain, err := s.secrets.Decrypt(pw)
		if err != nil {
			return "", nil, fmt.Errorf("decrypt registry %d: %w", r.ID, err)
		}
		auth := registry.AuthConfig{
			Username:      r.Username,
			Password:      string(plain),
			ServerAddress: r.URL,
		}
		blob, err := json.Marshal(auth)
		if err != nil {
			return "", nil, err
		}
		r.HasPassword = true
		return base64.URLEncoding.EncodeToString(blob), &r, nil
	}
	return "", nil, nil
}

// PlaintextAuth is like ResolveAuth but returns the raw AuthConfig —
// used by the test-login flow that calls cli.RegistryLogin() directly
// instead of tunneling through ImagePull.
func (s *Service) PlaintextAuth(ctx context.Context, id int64) (*registry.AuthConfig, error) {
	var r Registry
	var pw []byte
	var scope sql.NullString
	var lastAt sql.NullTime
	var lastOK sql.NullBool
	var lastErr string
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, url, COALESCE(username, ''),
		       password_encrypted,
		       scope_tags, last_tested_at, last_test_ok, COALESCE(last_test_error, ''),
		       created_at, updated_at
		  FROM registries WHERE id = ?`, id)
	if err := row.Scan(&r.ID, &r.Name, &r.URL, &r.Username,
		&pw, &scope, &lastAt, &lastOK, &lastErr, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if pw == nil {
		return nil, errors.New("registry has no stored password")
	}
	plain, err := s.secrets.Decrypt(pw)
	if err != nil {
		return nil, err
	}
	return &registry.AuthConfig{
		Username:      r.Username,
		Password:      string(plain),
		ServerAddress: r.URL,
	}, nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRegistry(r rowScanner) (*Registry, error) {
	var reg Registry
	var scope sql.NullString
	var lastAt sql.NullTime
	var lastOK sql.NullBool
	var lastErr string
	var hasPw int
	if err := r.Scan(&reg.ID, &reg.Name, &reg.URL, &reg.Username, &hasPw,
		&scope, &lastAt, &lastOK, &lastErr, &reg.CreatedAt, &reg.UpdatedAt); err != nil {
		return nil, err
	}
	reg.HasPassword = hasPw == 1
	reg.ScopeTags = parseScope(scope)
	if lastAt.Valid {
		t := lastAt.Time
		reg.LastTestedAt = &t
	}
	if lastOK.Valid {
		b := lastOK.Bool
		reg.LastTestOK = &b
	}
	reg.LastTestErr = lastErr
	return &reg, nil
}

func validateInput(in Input) error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(in.URL) == "" {
		return errors.New("url is required")
	}
	return nil
}

func marshalScope(tags []string) any {
	if len(tags) == 0 {
		return nil
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func parseScope(raw sql.NullString) []string {
	if !raw.Valid || raw.String == "" || raw.String == "null" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(raw.String), &out)
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") ||
		strings.Contains(s, "constraint failed") && strings.Contains(s, "unique")
}

func updateErr(err error) error {
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}
