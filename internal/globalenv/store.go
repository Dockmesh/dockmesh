// Package globalenv provides CRUD for global environment variables
// that get injected into every stack deploy. Users manage them via
// the WebGUI; the compose deploy flow calls Merged() to combine
// global vars with the stack's own .env content.
package globalenv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/secrets"
)

// Var is one row in the global_env table.
type Var struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	Group     string `json:"group_name"`
	Encrypted bool   `json:"encrypted"`
	// Unreadable marks a secret whose stored value can't be decrypted
	// (wrong key, encryption turned off); Value is empty then.
	Unreadable bool      `json:"unreadable,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// VarInput is the create/update payload.
type VarInput struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Group     string `json:"group_name"`
	Encrypted bool   `json:"encrypted,omitempty"`
}

// ErrDuplicateKey is returned by Create/Update when the key is taken.
var ErrDuplicateKey = errors.New("a global variable with that key already exists")

// ErrNotFound is returned by Get when the id doesn't exist.
var ErrNotFound = errors.New("variable not found")

// Store keeps the variables. Values of variables marked encrypted are
// stored sealed with sec and opened again on read.
type Store struct {
	db  *sql.DB
	sec *secrets.Service
}

func NewStore(db *sql.DB, sec *secrets.Service) *Store { return &Store{db: db, sec: sec} }

func (s *Store) List(ctx context.Context) ([]Var, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key, value, group_name, encrypted, created_at, updated_at
		 FROM global_env ORDER BY group_name, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Non-nil slice so the JSON response is [] not null — keeps the UI
	// from doing .length on null.
	out := make([]Var, 0)
	for rows.Next() {
		var v Var
		var enc int
		if err := rows.Scan(&v.ID, &v.Key, &v.Value, &v.Group, &enc, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		s.open(&v, enc)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Create(ctx context.Context, in VarInput) (*Var, error) {
	if in.Key == "" {
		return nil, fmt.Errorf("key required")
	}
	value, enc, err := s.storedValue(in)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO global_env (key, value, group_name, encrypted) VALUES (?, ?, ?, ?)`,
		in.Key, value, in.Group, enc)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: global_env.key") {
			return nil, ErrDuplicateKey
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.get(ctx, id)
}

func (s *Store) Update(ctx context.Context, id int64, in VarInput) (*Var, error) {
	value, enc, err := s.storedValue(in)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE global_env SET key = ?, value = ?, group_name = ?, encrypted = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		in.Key, value, in.Group, enc, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: global_env.key") {
			return nil, ErrDuplicateKey
		}
		return nil, err
	}
	return s.get(ctx, id)
}

// open decrypts the value of a variable marked encrypted. Plain variables
// are taken as stored, even if they happen to look sealed. A value that
// can't be decrypted marks the variable unreadable instead of failing, so
// the list still loads and the value can be set again.
func (s *Store) open(v *Var, enc int) {
	v.Encrypted = enc == 1
	if !v.Encrypted {
		return
	}
	plain, err := s.sec.OpenString(v.Value)
	if err != nil {
		slog.Warn("global env: can't decrypt value", "key", v.Key, "err", err)
		v.Value, v.Unreadable = "", true
		return
	}
	v.Value = plain
}

// storedValue returns the value as it goes into the database, sealed for
// a variable marked encrypted.
func (s *Store) storedValue(in VarInput) (string, int, error) {
	if !in.Encrypted {
		return in.Value, 0, nil
	}
	v, err := s.sec.SealString(in.Value)
	if err != nil {
		return "", 0, fmt.Errorf("encrypt value: %w", err)
	}
	return v, 1, nil
}

// EncryptFlagged seals the values of variables marked encrypted that are
// still stored in plaintext and returns how many it rewrote. It runs in
// one transaction and leaves sealed values alone, so it is safe to repeat.
func (s *Store) EncryptFlagged(ctx context.Context) (int, error) {
	if s.sec == nil || !s.sec.Enabled() {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, value FROM global_env WHERE encrypted = 1 AND value <> ''`)
	if err != nil {
		return 0, err
	}
	type plain struct {
		id    int64
		value string
	}
	var pending []plain
	for rows.Next() {
		var p plain
		if err := rows.Scan(&p.id, &p.value); err != nil {
			rows.Close()
			return 0, err
		}
		if !secrets.IsSealed(p.value) {
			pending = append(pending, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, p := range pending {
		sealed, err := s.sec.SealString(p.value)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE global_env SET value = ? WHERE id = ?`, sealed, p.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(pending), nil
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM global_env WHERE id = ?`, id)
	return err
}

func (s *Store) get(ctx context.Context, id int64) (*Var, error) {
	return s.Get(ctx, id)
}

// Get returns one row by id, ErrNotFound when absent.
func (s *Store) Get(ctx context.Context, id int64) (*Var, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, key, value, group_name, encrypted, created_at, updated_at FROM global_env WHERE id = ?`, id)
	var v Var
	var enc int
	if err := row.Scan(&v.ID, &v.Key, &v.Value, &v.Group, &enc, &v.CreatedAt, &v.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.open(&v, enc)
	return &v, nil
}

// Merged returns the global env vars as a single string in KEY=VALUE
// format, suitable for prepending to a stack's .env content. Stack-
// level vars take precedence over globals (appended after).
func (s *Store) Merged(ctx context.Context, stackEnv string) (string, error) {
	vars, err := s.List(ctx)
	if err != nil {
		return stackEnv, err
	}
	if len(vars) == 0 {
		return stackEnv, nil
	}
	// Build global lines, then append stack lines (stack overrides global).
	var lines []string
	for _, v := range vars {
		if v.Unreadable {
			return stackEnv, fmt.Errorf("global variable %s can't be decrypted", v.Key)
		}
		lines = append(lines, v.Key+"="+v.Value)
	}
	if stackEnv != "" {
		lines = append(lines, strings.Split(stackEnv, "\n")...)
	}
	return strings.Join(lines, "\n"), nil
}

// Groups returns distinct group names for the UI dropdown.
func (s *Store) Groups(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT group_name FROM global_env WHERE group_name != '' ORDER BY group_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
