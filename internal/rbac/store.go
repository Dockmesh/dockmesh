package rbac

import (
	"context"
	"database/sql"
	"sync"
)

// CustomRole is one row in the roles table + its permissions + its scope
// rows. Description is a one-line human blurb shown under the role name
// in the Roles UI.
type CustomRole struct {
	Name        string  `json:"name"`
	Display     string  `json:"display"`
	Description string  `json:"description,omitempty"`
	Builtin     bool    `json:"builtin"`
	Permissions []Perm  `json:"permissions"`
	// Scopes — empty list means "unscoped" (all hosts, all stacks).
	Scopes []Scope `json:"scopes,omitempty"`
}

// Scope is one entry in the role's scope list. ScopeType is one of
// "host" / "stack" / "host_tag". ScopeValue is the host_id, stack name,
// or host tag respectively. Effective scope is the UNION of all rows for
// a given role — a role can be scoped to "host=A or stack=monitoring".
type Scope struct {
	ScopeType  string `json:"scope_type"`
	ScopeValue string `json:"scope_value"`
}

// RoleInput is the create/update payload.
type RoleInput struct {
	Name        string  `json:"name"`
	Display     string  `json:"display"`
	Description string  `json:"description,omitempty"`
	Permissions []Perm  `json:"permissions"`
	Scopes      []Scope `json:"scopes,omitempty"`
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

// ScopeRequest describes the resource a request is operating on, for
// scope-check purposes. The fields are populated as much as the handler
// can determine — typically (host_id, stack_name) for a container or
// stack handler, (host_id) for a host-level handler, etc.
//
// Unset fields are treated as "not constrained on that dimension" — a
// host-only request matches a role scoped to "host=A" but NOT a role
// scoped to "stack=monitoring" (because no stack info was provided).
type ScopeRequest struct {
	HostID    string
	StackName string
	// HostTags is the list of tags carried by the host being acted on.
	// Caller fills it in by looking up the host from the host store.
	// Empty if the request isn't tied to a specific host (rare).
	HostTags []string
}

// InScope reports whether the given role's scope rows allow operating
// on the resource described by req. A role with NO scope rows is
// "unscoped" and matches everything (returns true). Otherwise the role
// matches if ANY of its scope rows match the request:
//
//   - scope_type=host with scope_value matching req.HostID
//   - scope_type=stack with scope_value matching req.StackName
//   - scope_type=host_tag with scope_value present in req.HostTags
//
// Built-in roles always have an empty scope list — they're always
// unscoped — so InScope() returns true for them in O(1).
func (s *Store) InScope(role string, req ScopeRequest) bool {
	s.mu.RLock()
	r, ok := s.cache[role]
	s.mu.RUnlock()
	if !ok {
		// Unknown role — no scope check possible, fall open. Allowed()
		// will already have rejected unknown roles upstream.
		return true
	}
	if len(r.Scopes) == 0 {
		return true
	}
	for _, sc := range r.Scopes {
		switch sc.ScopeType {
		case "host":
			if req.HostID != "" && sc.ScopeValue == req.HostID {
				return true
			}
		case "stack":
			if req.StackName != "" && sc.ScopeValue == req.StackName {
				return true
			}
		case "host_tag":
			for _, t := range req.HostTags {
				if t == sc.ScopeValue {
					return true
				}
			}
		}
	}
	return false
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
		`INSERT INTO roles (name, display, description, builtin) VALUES (?, ?, ?, 0)`,
		in.Name, in.Display, in.Description); err != nil {
		return err
	}
	for _, p := range in.Permissions {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_permissions (role_name, permission) VALUES (?, ?)`,
			in.Name, string(p)); err != nil {
			return err
		}
	}
	for _, sc := range in.Scopes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_scopes (role_name, scope_type, scope_value) VALUES (?, ?, ?)`,
			in.Name, sc.ScopeType, sc.ScopeValue); err != nil {
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
		`UPDATE roles SET display = ?, description = ? WHERE name = ?`,
		in.Display, in.Description, name); err != nil {
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
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM role_scopes WHERE role_name = ?`, name); err != nil {
		return err
	}
	for _, sc := range in.Scopes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_scopes (role_name, scope_type, scope_value) VALUES (?, ?, ?)`,
			name, sc.ScopeType, sc.ScopeValue); err != nil {
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
		SELECT r.name, r.display, COALESCE(r.description, ''), r.builtin
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
		if err := rows.Scan(&r.Name, &r.Display, &r.Description, &builtin); err != nil {
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
	// Load scopes — graceful skip if migration hasn't run yet (the
	// table won't exist on a freshly-cloned dev DB pre-038).
	scopeRows, err := s.db.QueryContext(ctx,
		`SELECT role_name, scope_type, scope_value FROM role_scopes ORDER BY role_name, scope_type, scope_value`)
	if err == nil {
		defer scopeRows.Close()
		for scopeRows.Next() {
			var roleName string
			var sc Scope
			if err := scopeRows.Scan(&roleName, &sc.ScopeType, &sc.ScopeValue); err != nil {
				return nil, err
			}
			if r, ok := roleMap[roleName]; ok {
				r.Scopes = append(r.Scopes, sc)
			}
		}
	}
	out := make([]CustomRole, 0, len(order))
	for _, name := range order {
		out = append(out, *roleMap[name])
	}
	return out, nil
}
