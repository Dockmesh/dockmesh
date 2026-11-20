package clusters

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Access is everything that grants or restricts access on a cluster's
// scope. Deleting the cluster removes it: the cluster id is the Swarm's,
// so anything left behind would apply again to a cluster re-created for
// the same Swarm.
type Access struct {
	Assignments []ScopedAssignment `json:"assignments"`
	Invites     []ScopedInvite     `json:"invites"`
	Mappings    []ScopedMapping    `json:"mappings"`
	Tokens      []ScopedToken      `json:"tokens"`
}

type ScopedAssignment struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Via is the IdP group of an assignment the group mapping made.
	Via string `json:"via,omitempty"`
}

// ScopedInvite is an unused invite with at least one scope on the
// cluster. Expired is set when it lists nothing else, so deleting the
// cluster makes the invite unusable instead of widening it.
type ScopedInvite struct {
	ID        int64  `json:"id"`
	EmailHint string `json:"email_hint,omitempty"`
	Role      string `json:"role"`
	Expired   bool   `json:"expired"`
}

type ScopedMapping struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Group    string `json:"group"`
	Role     string `json:"role"`
}

// ScopedToken is an API token restricted to the cluster; it is revoked,
// never left without its restriction.
type ScopedToken struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner,omitempty"`
}

// Counts sums Access up per kind, for the audit entry.
func (a Access) Counts() map[string]int {
	return map[string]int{
		"assignments": len(a.Assignments), "invites": len(a.Invites),
		"mappings": len(a.Mappings), "tokens": len(a.Tokens),
	}
}

var mappingTables = []struct{ kind, mappings, providers string }{
	{"oidc", "oidc_provider_group_mappings", "oidc_providers"},
	{"saml", "saml_provider_group_mappings", "saml_providers"},
	{"ldap", "ldap_provider_group_mappings", "ldap_providers"},
	{"oauth2", "oauth2_provider_group_mappings", "oauth2_providers"},
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// AccessOf lists what grants or restricts access on cluster id.
func (s *Service) AccessOf(ctx context.Context, id string) (Access, error) {
	ok, err := s.exists(ctx, id)
	if err != nil {
		return Access{}, err
	}
	if !ok {
		return Access{}, ErrNotFound
	}
	a, _, err := accessOf(ctx, s.db, id)
	return a, err
}

// inviteRewrite is the scope list an invite keeps once the cluster is gone.
type inviteRewrite struct {
	id   int64
	keep []rbac.Scope
}

func accessOf(ctx context.Context, q querier, id string) (Access, []inviteRewrite, error) {
	a := Access{Assignments: []ScopedAssignment{}, Invites: []ScopedInvite{}, Mappings: []ScopedMapping{}, Tokens: []ScopedToken{}}
	err := scanRows(ctx, q, func(r *sql.Rows) error {
		var x ScopedAssignment
		if err := r.Scan(&x.ID, &x.Username, &x.Role, &x.Via); err != nil {
			return err
		}
		a.Assignments = append(a.Assignments, x)
		return nil
	}, `SELECT ra.id, COALESCE(u.username, ra.user_id), ra.role_name, ra.via FROM role_assignments ra
		LEFT JOIN users u ON u.id = ra.user_id
		WHERE ra.scope_type = ? AND ra.scope_host = ? ORDER BY u.username, ra.role_name, ra.id`, rbac.ScopeCluster, id)
	if err != nil {
		return Access{}, nil, err
	}
	for _, mt := range mappingTables {
		err := scanRows(ctx, q, func(r *sql.Rows) error {
			x := ScopedMapping{Kind: mt.kind}
			if err := r.Scan(&x.Provider, &x.Group, &x.Role); err != nil {
				return err
			}
			a.Mappings = append(a.Mappings, x)
			return nil
		}, `SELECT COALESCE(p.display_name, ''), m.group_value, m.role_name FROM `+mt.mappings+` m
			LEFT JOIN `+mt.providers+` p ON p.id = m.provider_id
			WHERE m.scope_type = ? AND m.scope_host = ? ORDER BY p.display_name, m.group_value, m.role_name`, rbac.ScopeCluster, id)
		if err != nil {
			return Access{}, nil, err
		}
	}
	err = scanRows(ctx, q, func(r *sql.Rows) error {
		var x ScopedToken
		if err := r.Scan(&x.ID, &x.Name, &x.Owner); err != nil {
			return err
		}
		a.Tokens = append(a.Tokens, x)
		return nil
	}, `SELECT t.id, t.name, COALESCE(u.username, '') FROM api_tokens t
		LEFT JOIN users u ON u.id = t.created_by_user_id
		WHERE t.revoked_at IS NULL AND t.scope_type = ? AND t.scope_host = ? ORDER BY t.name, t.id`, rbac.ScopeCluster, id)
	if err != nil {
		return Access{}, nil, err
	}
	var rewrites []inviteRewrite
	err = scanRows(ctx, q, func(r *sql.Rows) error {
		var x ScopedInvite
		var raw string
		if err := r.Scan(&x.ID, &x.EmailHint, &x.Role, &raw); err != nil {
			return err
		}
		var scopes []rbac.Scope
		_ = json.Unmarshal([]byte(raw), &scopes)
		onCluster := func(sc rbac.Scope) bool { return sc.Type == rbac.ScopeCluster && sc.Host == id }
		if !slices.ContainsFunc(scopes, onCluster) {
			return nil
		}
		keep := slices.DeleteFunc(slices.Clone(scopes), onCluster)
		x.Expired = len(keep) == 0
		a.Invites = append(a.Invites, x)
		rewrites = append(rewrites, inviteRewrite{id: x.ID, keep: keep})
		return nil
	}, `SELECT id, email_hint, role, scopes_json FROM invite_tokens
		WHERE used_at IS NULL AND scopes_json LIKE '%"cluster"%' ORDER BY id`)
	if err != nil {
		return Access{}, nil, err
	}
	return a, rewrites, nil
}

func scanRows(ctx context.Context, q querier, each func(*sql.Rows) error, query string, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// removeAccess deletes what accessOf found, inside tx.
func removeAccess(ctx context.Context, tx *sql.Tx, id string, invites []inviteRewrite) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM role_assignments WHERE scope_type = ? AND scope_host = ?`, rbac.ScopeCluster, id); err != nil {
		return err
	}
	for _, mt := range mappingTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+mt.mappings+` WHERE scope_type = ? AND scope_host = ?`, rbac.ScopeCluster, id); err != nil { // #nosec G202 -- fixed table names
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = CURRENT_TIMESTAMP
		WHERE revoked_at IS NULL AND scope_type = ? AND scope_host = ?`, rbac.ScopeCluster, id); err != nil {
		return err
	}
	for _, inv := range invites {
		if len(inv.keep) == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE invite_tokens SET scopes_json = '[]', expires_at = CURRENT_TIMESTAMP WHERE id = ?`, inv.id); err != nil {
				return err
			}
			continue
		}
		b, err := json.Marshal(inv.keep)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invite_tokens SET scopes_json = ? WHERE id = ?`, string(b), inv.id); err != nil {
			return err
		}
	}
	return nil
}
