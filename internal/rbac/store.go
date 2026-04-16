package rbac

import (
	"context"
	"database/sql"
	"sync"
)

// CustomRole is one row in the roles table + its permissions.
type CustomRole struct {
	Name        string `json:"name"`
	Display     string `json:"display"`
	Builtin     bool   `json:"builtin"`
	Permissions []Perm `json:"permissions"`
}

// RoleInput is the create/update payload.
type RoleInput struct {
	Name        string `json:"name"`
	Display     string `json:"display"`
	Permissions []Perm `json:"permissions"`
}

// Store provides DB-backed role CRUD with an in-memory cache so
// Allowed() stays fast (called on every request via middleware).
type Store struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache map[string]*CustomRole // role name → role
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, cache: make(map[string]*CustomRole)}
}

// Load populates the in-memory cache from the DB. Called on startup
// and after any CRUD operation.
func (s *Store) Load(ctx context.Context) error {
	roles, err := s.listFromDB(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cache = make(map[string]*CustomRole, len(roles))
	for _, r := range roles {
		r := r
		s.cache[r.Name] = &r
	}
	s.mu.Unlock()
	return nil
}

// AllowedDB checks the cached custom roles first. If the role exists
// in the DB cache, use its permissions. Otherwise fall back to the
// hardcoded rolePerms map (backwards compat for pre-migration setups).
func (s *Store) AllowedDB(role string, perm Perm) bool {
	s.mu.RLock()
	r, ok := s.cache[role]
	s.mu.RUnlock()
	if ok {
		for _, p := range r.Permissions {
			if p == perm {
				return true
			}
		}
		return false
	}
	// Fall back to hardcoded.
	return Allowed(role, perm)
}

// List returns all roles (cached).
func (s *Store) List() []CustomRole {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CustomRole, 0, len(s.cache))
	for _, r := range s.cache {
		out = append(out, *r)
	}
	return out
}

// Get returns a single role by name.
func (s *Store) Get(name string) (*CustomRole, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.cache[name]
	if !ok {
		return nil, false
	}
	return r, true
}

// Create adds a new custom role.
func (s *Store) Create(ctx context.Context, in RoleInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO roles (name, display, builtin) VALUES (?, ?, 0)`,
		in.Name, in.Display); err != nil {
		return err
	}
	for _, p := range in.Permissions {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_permissions (role_name, permission) VALUES (?, ?)`,
			in.Name, string(p)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.Load(ctx)
}

// Update modifies an existing custom role (not builtins).
func (s *Store) Update(ctx context.Context, name string, in RoleInput) error {
	s.mu.RLock()
	existing, ok := s.cache[name]
	s.mu.RUnlock()
	if !ok {
		return sql.ErrNoRows
	}
	if existing.Builtin {
		return sql.ErrNoRows // can't edit built-in roles
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE roles SET display = ? WHERE name = ?`,
		in.Display, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM role_permissions WHERE role_name = ?`, name); err != nil {
		return err
	}
	for _, p := range in.Permissions {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_permissions (role_name, permission) VALUES (?, ?)`,
			name, string(p)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.Load(ctx)
}

// Delete removes a custom role (not builtins).
func (s *Store) Delete(ctx context.Context, name string) error {
	s.mu.RLock()
	existing, ok := s.cache[name]
	s.mu.RUnlock()
	if !ok {
		return sql.ErrNoRows
	}
	if existing.Builtin {
		return sql.ErrNoRows
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM roles WHERE name = ? AND builtin = 0`, name); err != nil {
		return err
	}
	return s.Load(ctx)
}

func (s *Store) listFromDB(ctx context.Context) ([]CustomRole, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.name, r.display, r.builtin
		FROM roles r ORDER BY r.builtin DESC, r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roleMap := make(map[string]*CustomRole)
	var order []string
	for rows.Next() {
		var r CustomRole
		var builtin int
		if err := rows.Scan(&r.Name, &r.Display, &builtin); err != nil {
			return nil, err
		}
		r.Builtin = builtin == 1
		roleMap[r.Name] = &r
		order = append(order, r.Name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Load permissions.
	permRows, err := s.db.QueryContext(ctx,
		`SELECT role_name, permission FROM role_permissions ORDER BY role_name, permission`)
	if err != nil {
		return nil, err
	}
	defer permRows.Close()
	for permRows.Next() {
		var roleName, perm string
		if err := permRows.Scan(&roleName, &perm); err != nil {
			return nil, err
		}
		if r, ok := roleMap[roleName]; ok {
			r.Permissions = append(r.Permissions, Perm(perm))
		}
	}
	out := make([]CustomRole, 0, len(order))
	for _, name := range order {
		out = append(out, *roleMap[name])
	}
	return out, nil
}
