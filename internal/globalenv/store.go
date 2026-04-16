// Package globalenv provides CRUD for global environment variables
// that get injected into every stack deploy. Users manage them via
// the WebGUI; the compose deploy flow calls Merged() to combine
// global vars with the stack's own .env content.
package globalenv

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Var is one row in the global_env table.
type Var struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Group     string    `json:"group_name"`
	Encrypted bool      `json:"encrypted"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// VarInput is the create/update payload.
type VarInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Group string `json:"group_name"`
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) List(ctx context.Context) ([]Var, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, key, value, group_name, encrypted, created_at, updated_at
		 FROM global_env ORDER BY group_name, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Var
	for rows.Next() {
		var v Var
		var enc int
		if err := rows.Scan(&v.ID, &v.Key, &v.Value, &v.Group, &enc, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.Encrypted = enc == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Create(ctx context.Context, in VarInput) (*Var, error) {
	if in.Key == "" {
		return nil, fmt.Errorf("key required")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO global_env (key, value, group_name) VALUES (?, ?, ?)`,
		in.Key, in.Value, in.Group)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.get(ctx, id)
}

func (s *Store) Update(ctx context.Context, id int64, in VarInput) (*Var, error) {
	_, err := s.db.ExecContext(ctx,
		`UPDATE global_env SET key = ?, value = ?, group_name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		in.Key, in.Value, in.Group, id)
	if err != nil {
		return nil, err
	}
	return s.get(ctx, id)
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM global_env WHERE id = ?`, id)
	return err
}

func (s *Store) get(ctx context.Context, id int64) (*Var, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, key, value, group_name, encrypted, created_at, updated_at FROM global_env WHERE id = ?`, id)
	var v Var
	var enc int
	if err := row.Scan(&v.ID, &v.Key, &v.Value, &v.Group, &enc, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.Encrypted = enc == 1
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
