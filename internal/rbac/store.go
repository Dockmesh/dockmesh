package rbac

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Role is a stored role: levels per area plus extras. Permissions is
// derived from them and only informs clients.
type Role struct {
	Name        string   `json:"name"`
	Display     string   `json:"display"`
	Description string   `json:"description"`
	Builtin     bool     `json:"builtin"`
	Levels      Levels   `json:"levels"`
	Extras      []string `json:"extras"`
	Permissions []Perm   `json:"permissions"`
}

// RoleInput is the create/update payload of a custom role.
type RoleInput struct {
	Name        string   `json:"name"`
	Display     string   `json:"display"`
	Description string   `json:"description"`
	Levels      Levels   `json:"levels"`
	Extras      []string `json:"extras"`
}

var (
	ErrRoleNotFound = errors.New("role not found")
	ErrRoleBuiltin  = errors.New("built-in roles can't be changed; duplicate it instead")
	ErrRoleInUse    = errors.New("role is still assigned")
	ErrNotFound     = errors.New("assignment not found")
)

// Store keeps roles and assignments in memory, backed by the database.
// Every write reloads the cache, so checks never hit the database.
type Store struct {
	db *sql.DB

	mu          sync.RWMutex
	roles       map[string]*Role
	rolePerms   map[string]map[Perm]bool
	assignments map[string][]Assignment // user id → assignments
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, roles: map[string]*Role{}, rolePerms: map[string]map[Perm]bool{}, assignments: map[string][]Assignment{}}
}

// SeedBuiltins writes the built-in role definitions, so a release that
// changes them takes effect on start.
func (s *Store) SeedBuiltins(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := SeedBuiltinsTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// SeedBuiltinsTx is SeedBuiltins inside tx.
func SeedBuiltinsTx(ctx context.Context, tx *sql.Tx) error {
	for _, r := range BuiltinRoles {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO roles (name, display, description, builtin) VALUES (?, ?, ?, 1)
			ON CONFLICT(name) DO UPDATE SET display = excluded.display, description = excluded.description, builtin = 1`,
			r.Name, r.Display, r.Description); err != nil {
			return err
		}
		if err := writeLevels(ctx, tx, r.Name, r.Levels, r.Extras); err != nil {
			return err
		}
	}
	return nil
}

func writeLevels(ctx context.Context, tx *sql.Tx, name string, levels Levels, extras []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_levels WHERE role_name = ?`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_extras WHERE role_name = ?`, name); err != nil {
		return err
	}
	for area, level := range levels {
		if level == "" || level == LevelNone {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_levels (role_name, area, level) VALUES (?, ?, ?)`, name, area, level); err != nil {
			return err
		}
	}
	for _, e := range extras {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO role_extras (role_name, extra) VALUES (?, ?)`, name, e); err != nil {
			return err
		}
	}
	return nil
}

// Load refreshes the cache from the database.
func (s *Store) Load(ctx context.Context) error {
	roles, err := loadRoles(ctx, s.db)
	if err != nil {
		return err
	}
	as, err := loadAssignments(ctx, s.db)
	if err != nil {
		return err
	}
	perms := make(map[string]map[Perm]bool, len(roles))
	byName := make(map[string]*Role, len(roles))
	for i := range roles {
		r := roles[i]
		set := PermsFor(r.Levels, r.Extras)
		r.Permissions = SortedPerms(set)
		perms[r.Name] = set
		byName[r.Name] = &r
	}
	byUser := map[string][]Assignment{}
	for _, a := range as {
		byUser[a.UserID] = append(byUser[a.UserID], a)
	}
	s.mu.Lock()
	s.roles, s.rolePerms, s.assignments = byName, perms, byUser
	s.mu.Unlock()
	return nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// LoadRoles reads every role definition straight from the database.
func LoadRoles(ctx context.Context, q querier) ([]Role, error) { return loadRoles(ctx, q) }

func loadRoles(ctx context.Context, q querier) ([]Role, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, display, COALESCE(description, ''), builtin FROM roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]*Role{}
	var order []string
	for rows.Next() {
		var r Role
		var builtin int
		if err := rows.Scan(&r.Name, &r.Display, &r.Description, &builtin); err != nil {
			return nil, err
		}
		r.Builtin = builtin == 1
		r.Levels = Levels{}
		r.Extras = []string{}
		byName[r.Name] = &r
		order = append(order, r.Name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	lrows, err := q.QueryContext(ctx, `SELECT role_name, area, level FROM role_levels`)
	if err != nil {
		return nil, err
	}
	defer lrows.Close()
	for lrows.Next() {
		var name, area, level string
		if err := lrows.Scan(&name, &area, &level); err != nil {
			return nil, err
		}
		if r, ok := byName[name]; ok {
			r.Levels[area] = level
		}
	}
	if err := lrows.Err(); err != nil {
		return nil, err
	}
	erows, err := q.QueryContext(ctx, `SELECT role_name, extra FROM role_extras ORDER BY extra`)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var name, extra string
		if err := erows.Scan(&name, &extra); err != nil {
			return nil, err
		}
		if r, ok := byName[name]; ok {
			r.Extras = append(r.Extras, extra)
		}
	}
	if err := erows.Err(); err != nil {
		return nil, err
	}
	out := make([]Role, 0, len(order))
	for _, n := range order {
		r := byName[n]
		for _, a := range Areas {
			if _, ok := r.Levels[a.Key]; !ok {
				r.Levels[a.Key] = LevelNone
			}
		}
		out = append(out, *r)
	}
	return out, nil
}

func loadAssignments(ctx context.Context, q querier) ([]Assignment, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, user_id, role_name, scope_type, scope_host, scope_value, source, via,
		       COALESCE(created_at, ''), COALESCE(created_by, '')
		  FROM role_assignments ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Assignment
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.ID, &a.UserID, &a.Role, &a.Scope.Type, &a.Scope.Host, &a.Scope.Value, &a.Source, &a.Via, &a.CreatedAt, &a.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Roles returns every role, built-ins first.
func (s *Store) Roles() []Role {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Role, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return out[i].Builtin
		}
		return builtinOrder(out[i].Name) < builtinOrder(out[j].Name) || (builtinOrder(out[i].Name) == builtinOrder(out[j].Name) && out[i].Name < out[j].Name)
	})
	return out
}

func builtinOrder(name string) int {
	for i, r := range BuiltinRoles {
		if r.Name == name {
			return i
		}
	}
	return len(BuiltinRoles)
}

// Role returns one role.
func (s *Store) Role(name string) (Role, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roles[name]
	if !ok {
		return Role{}, false
	}
	return *r, true
}

// RolePerms returns the permission set of a role.
func (s *Store) RolePerms(name string) (map[Perm]bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.rolePerms[name]
	return p, ok
}

// CreateRole adds a custom role.
func (s *Store) CreateRole(ctx context.Context, in RoleInput) error {
	if err := ValidateLevels(in.Levels, in.Extras); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO roles (name, display, description, builtin) VALUES (?, ?, ?, 0)`,
		in.Name, in.Display, in.Description); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("a role named %q already exists", in.Name)
		}
		return err
	}
	if err := writeLevels(ctx, tx, in.Name, in.Levels, in.Extras); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.Load(ctx)
}

// UpdateRole replaces the definition of a custom role.
func (s *Store) UpdateRole(ctx context.Context, name string, in RoleInput) error {
	cur, ok := s.Role(name)
	if !ok {
		return ErrRoleNotFound
	}
	if cur.Builtin {
		return ErrRoleBuiltin
	}
	if err := ValidateLevels(in.Levels, in.Extras); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE roles SET display = ?, description = ? WHERE name = ? AND builtin = 0`,
		in.Display, in.Description, name); err != nil {
		return err
	}
	if err := writeLevels(ctx, tx, name, in.Levels, in.Extras); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.Load(ctx)
}

// DeleteRole removes a custom role that nobody holds any more.
func (s *Store) DeleteRole(ctx context.Context, name string) error {
	cur, ok := s.Role(name)
	if !ok {
		return ErrRoleNotFound
	}
	if cur.Builtin {
		return ErrRoleBuiltin
	}
	if n := len(s.RoleAssignments(name)); n > 0 {
		return fmt.Errorf("%w: remove its %d assignment(s) first", ErrRoleInUse, n)
	}
	var mapped int
	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM oidc_provider_group_mappings WHERE role_name = ?1)
		     + (SELECT COUNT(*) FROM saml_provider_group_mappings WHERE role_name = ?1)
		     + (SELECT COUNT(*) FROM ldap_provider_group_mappings WHERE role_name = ?1)
		     + (SELECT COUNT(*) FROM oauth2_provider_group_mappings WHERE role_name = ?1)`, name).Scan(&mapped); err != nil {
		return err
	}
	if mapped > 0 {
		return fmt.Errorf("%w: %d identity provider group mapping(s) use it", ErrRoleInUse, mapped)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE name = ? AND builtin = 0`, name); err != nil {
		return err
	}
	return s.Load(ctx)
}

// UserAssignments returns the assignments of one user.
func (s *Store) UserAssignments(userID string) []Assignment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Assignment(nil), s.assignments[userID]...)
}

// AllAssignments returns every assignment.
func (s *Store) AllAssignments() []Assignment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Assignment
	for _, as := range s.assignments {
		out = append(out, as...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RoleAssignments returns the assignments of one role.
func (s *Store) RoleAssignments(role string) []Assignment {
	var out []Assignment
	for _, a := range s.AllAssignments() {
		if a.Role == role {
			out = append(out, a)
		}
	}
	return out
}

// Assignment returns one assignment by id.
func (s *Store) Assignment(id int64) (Assignment, bool) {
	for _, a := range s.AllAssignments() {
		if a.ID == id {
			return a, true
		}
	}
	return Assignment{}, false
}

// AddAssignment stores a manual assignment and returns it with its id.
// Adding an assignment that already exists returns the existing one.
func (s *Store) AddAssignment(ctx context.Context, a Assignment) (Assignment, error) {
	if err := a.Scope.Validate(); err != nil {
		return Assignment{}, err
	}
	if _, ok := s.Role(a.Role); !ok {
		return Assignment{}, ErrRoleNotFound
	}
	if a.Source == "" {
		a.Source = SourceManual
	}
	for _, cur := range s.UserAssignments(a.UserID) {
		if cur.Role == a.Role && cur.Scope == a.Scope && cur.Source == a.Source && cur.Via == a.Via {
			return cur, nil
		}
	}
	var createdBy any
	if a.CreatedBy != "" {
		createdBy = a.CreatedBy
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO role_assignments (user_id, role_name, scope_type, scope_host, scope_value, source, via, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.UserID, a.Role, a.Scope.Type, a.Scope.Host, a.Scope.Value, a.Source, a.Via, createdBy)
	if err != nil {
		return Assignment{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Assignment{}, err
	}
	if err := s.Load(ctx); err != nil {
		return Assignment{}, err
	}
	out, _ := s.Assignment(id)
	return out, nil
}

// RemoveAssignment deletes one assignment.
func (s *Store) RemoveAssignment(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM role_assignments WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return s.Load(ctx)
}

// ReplaceIdPAssignments swaps the identity-provider assignments of a user
// for want, leaving manual assignments alone.
func (s *Store) ReplaceIdPAssignments(ctx context.Context, userID string, want []Assignment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_assignments WHERE user_id = ? AND source = ?`, userID, SourceIdP); err != nil {
		return err
	}
	for _, a := range want {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO role_assignments (user_id, role_name, scope_type, scope_host, scope_value, source, via)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			userID, a.Role, a.Scope.Type, a.Scope.Host, a.Scope.Value, SourceIdP, a.Via); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.Load(ctx)
}

// AccessFor evaluates the assignments of a user.
func (s *Store) AccessFor(userID string) *Access {
	return s.AccessForAssignments(s.UserAssignments(userID))
}

// AccessForAssignments evaluates a hypothetical assignment list, e.g. to
// check a change before it is stored.
func (s *Store) AccessForAssignments(as []Assignment) *Access {
	acc := &Access{Authenticated: true}
	for _, a := range as {
		perms, ok := s.RolePerms(a.Role)
		if !ok {
			continue
		}
		g := Grant{Perms: perms, Assignment: a}
		if !a.Scope.IsGlobal() {
			g.Scopes = []Scope{a.Scope}
		}
		acc.Grants = append(acc.Grants, g)
	}
	return acc
}

// RoleAccess is the access of one role on everything, for legacy API
// tokens that have no owner.
func (s *Store) RoleAccess(role string) *Access {
	return s.AccessForAssignments([]Assignment{{Role: role, Scope: Global}})
}

// IsAdminRole reports whether a role holds every permission.
func (s *Store) IsAdminRole(name string) bool {
	perms, ok := s.RolePerms(name)
	if !ok {
		return false
	}
	for _, p := range AllPerms() {
		if !IsBase(p) && !perms[p] {
			return false
		}
	}
	return true
}

// IsGlobalAdmin reports whether a is an admin role on everything.
func (s *Store) IsGlobalAdmin(a Assignment) bool {
	return a.Scope.IsGlobal() && s.IsAdminRole(a.Role)
}
