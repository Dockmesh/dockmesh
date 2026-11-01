package rbac

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// legacyEquivalents lists what a permission of the old model let a
// caller do in terms of today's permissions; routes moved between
// permissions, so one old permission can stand for several new ones.
var legacyEquivalents = map[Perm][]Perm{
	PermContainersView:   {PermContainersView, PermContainersEnv},
	PermContainersUpdate: {PermContainersUpdate, PermContainersDelete, PermContainersUpgrade},
	PermContainersExec:   {PermContainersExec, PermContainersFiles},
	PermStacksView:       {PermStacksView, PermContainersEnv},
	PermStacksDeploy:     {PermStacksDeploy, PermStacksOperate},
	PermStacksUpdate:     {PermStacksUpdate, PermStacksGit},
	PermBackupsUpdate:    {PermBackupsUpdate, PermBackupsRun},
	PermAlertsView:       {PermAlertsView, PermAlertsHistory},
	"volumes.read_file":  {PermVolumesBrowse},
}

// LegacyEffective expands an old permission set into today's permissions.
// The template catalog was readable by every signed-in user, and the
// backup key could be exported by unscoped callers holding both
// system.update and backups.restore.
func LegacyEffective(old map[Perm]bool) map[Perm]bool {
	out := map[Perm]bool{PermTemplatesView: true}
	for p := range old {
		if eq, ok := legacyEquivalents[p]; ok {
			for _, q := range eq {
				out[q] = true
			}
			continue
		}
		out[p] = true
	}
	if old[PermSystemUpdate] && old[PermBackupsRestore] {
		out[PermBackupsKeyExport] = true
	}
	return out
}

// levelsCovering picks, per area, the highest level whose permissions
// (including those of every lower level) are all in have, and every extra
// fully in have. The result never grants more than have.
func levelsCovering(have map[Perm]bool) (Levels, []string) {
	levels := Levels{}
	for _, a := range Areas {
		levels[a.Key] = LevelNone
		for i := 1; i < len(a.Levels); i++ {
			ok := true
			for _, p := range a.Levels[i].Perms {
				ok = ok && have[p]
			}
			if !ok {
				break
			}
			levels[a.Key] = a.Levels[i].Key
		}
	}
	var extras []string
	for _, e := range Extras {
		ok := true
		for _, p := range e.Perms {
			ok = ok && have[p]
		}
		if ok {
			extras = append(extras, e.Key)
		}
	}
	return levels, extras
}

// MigrationChange is one line of the upgrade notice.
type MigrationChange struct {
	Subject string // "role Foo", "user alice", "API token ci"
	Text    string
}

type legacyRole struct {
	name   string
	perms  map[Perm]bool
	scopes []legacyScope
}

type legacyScope struct{ typ, value string }

type legacyWorld struct {
	hosts       []string            // every known host id
	hostTags    map[string][]string // host id → tags
	stackHost   map[string]string   // deployed stack → host
	roles       map[string]*legacyRole
	roleOrder   []string
	labelOfHost map[string]string
}

func (w *legacyWorld) hostLabel(id string) string {
	if l := w.labelOfHost[id]; l != "" && l != id {
		return l
	}
	return id
}

func (w *legacyWorld) hostHasAnyTag(host string, tags []string) bool {
	for _, t := range w.hostTags[host] {
		if slices.Contains(tags, t) {
			return true
		}
	}
	return false
}

// scopesFor turns a role's scope rows and a user's scope tags into the
// assignment scopes that give the same access. exact is false when the
// result had to be narrowed: role scopes combined with user tags are
// resolved to the hosts that match both today, so hosts tagged later are
// not covered, and undeployed stacks drop out.
func (w *legacyWorld) scopesFor(roleScopes []legacyScope, userTags []string) (out []Scope, exact bool) {
	exact = true
	switch {
	case len(roleScopes) == 0 && len(userTags) == 0:
		return []Scope{Global}, true
	case len(roleScopes) == 0:
		for _, t := range userTags {
			out = append(out, Scope{Type: ScopeHostTag, Value: t})
		}
		return out, true
	}
	seen := map[Scope]bool{}
	add := func(s Scope) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, rs := range roleScopes {
		switch rs.typ {
		case "host":
			if len(userTags) == 0 || w.hostHasAnyTag(rs.value, userTags) {
				add(Scope{Type: ScopeHost, Host: rs.value})
			} else {
				exact = false
			}
		case "host_tag":
			if len(userTags) == 0 {
				add(Scope{Type: ScopeHostTag, Value: rs.value})
				continue
			}
			exact = false
			for _, h := range w.hosts {
				if slices.Contains(w.hostTags[h], rs.value) && w.hostHasAnyTag(h, userTags) {
					add(Scope{Type: ScopeHost, Host: h})
				}
			}
		case "stack":
			host := w.stackHost[rs.value]
			if len(userTags) == 0 {
				add(Scope{Type: ScopeStack, Host: host, Value: rs.value})
				continue
			}
			if host != "" && w.hostHasAnyTag(host, userTags) {
				add(Scope{Type: ScopeStack, Host: host, Value: rs.value})
			}
			exact = false
		}
	}
	return out, exact
}

func scopesText(w *legacyWorld, ss []Scope) string {
	if len(ss) == 0 {
		return "nothing"
	}
	parts := make([]string, 0, len(ss))
	for _, s := range ss {
		switch s.Type {
		case ScopeHost:
			parts = append(parts, "host "+w.hostLabel(s.Host))
		case ScopeStack:
			if s.Host == "" {
				parts = append(parts, "stack "+s.Value)
			} else {
				parts = append(parts, "stack "+s.Value+" on "+w.hostLabel(s.Host))
			}
		default:
			parts = append(parts, s.String())
		}
	}
	return strings.Join(parts, ", ")
}

func legacyScopeText(w *legacyWorld, roleScopes []legacyScope, userTags []string) string {
	var parts []string
	for _, rs := range roleScopes {
		switch rs.typ {
		case "host":
			parts = append(parts, "host "+w.hostLabel(rs.value))
		case "host_tag":
			parts = append(parts, "host tag "+rs.value)
		default:
			parts = append(parts, "stack "+rs.value)
		}
	}
	s := "everything"
	if len(parts) > 0 {
		s = strings.Join(parts, " or ")
	}
	if len(userTags) > 0 {
		s += ", limited to hosts tagged " + strings.Join(userTags, " or ")
	}
	return s
}

// lostText lists what a role no longer grants, grouped by area.
func lostText(lost map[Perm]bool) string {
	var parts []string
	seen := map[Perm]bool{}
	group := func(label string, perms []Perm) {
		var names []string
		for _, p := range perms {
			if lost[p] && !seen[p] {
				seen[p] = true
				names = append(names, string(p))
			}
		}
		if len(names) > 0 {
			parts = append(parts, label+" ("+strings.Join(names, ", ")+")")
		}
	}
	for _, a := range Areas {
		var perms []Perm
		for _, l := range a.Levels {
			perms = append(perms, l.Perms...)
		}
		group(a.Label, perms)
	}
	for _, e := range Extras {
		group("extra "+e.Key, e.Perms)
	}
	return strings.Join(parts, "; ")
}

// diffPerms is before minus after, without the base rights and the
// template catalog every account could read.
func diffPerms(before, after map[Perm]bool) map[Perm]bool {
	out := map[Perm]bool{}
	for p := range before {
		if !after[p] && !IsBase(p) && p != PermTemplatesView {
			out[p] = true
		}
	}
	return out
}

func levelsText(levels Levels, extras []string) string {
	var parts []string
	for _, a := range Areas {
		if l := levels[a.Key]; l != "" && l != LevelNone {
			for _, lv := range a.Levels {
				if lv.Key == l {
					parts = append(parts, a.Label+" "+strings.ToLower(lv.Label))
				}
			}
		}
	}
	for _, e := range extras {
		parts = append(parts, "extra "+e)
	}
	if len(parts) == 0 {
		return "no access"
	}
	return strings.Join(parts, ", ")
}

// MigrateLegacy moves the role/scope model onto assignments. It runs in
// the migration's transaction, after the new tables exist, and drops the
// old tables and columns last; any error rolls the whole upgrade back.
func MigrateLegacy(ctx context.Context, tx *sql.Tx) error {
	w, err := loadLegacyWorld(ctx, tx)
	if err != nil {
		return err
	}
	var changes []MigrationChange

	newPerms := map[string]map[Perm]bool{}
	for _, b := range BuiltinRoles {
		newPerms[b.Name] = PermsFor(b.Levels, b.Extras)
	}
	if err := SeedBuiltinsTx(ctx, tx); err != nil {
		return err
	}
	builtinUsers, err := countUsersByRole(ctx, tx)
	if err != nil {
		return err
	}
	for _, name := range w.roleOrder {
		lr := w.roles[name]
		before := LegacyEffective(lr.perms)
		if _, isBuiltin := BuiltinRole(name); isBuiltin {
			if builtinUsers[name] == 0 {
				continue
			}
			if lost := lostText(diffPerms(before, newPerms[name])); lost != "" {
				changes = append(changes, MigrationChange{Subject: "built-in role " + name,
					Text: fmt.Sprintf("now follows the new definition and no longer allows: %s", lost)})
			}
			continue
		}
		levels, extras := levelsCovering(before)
		if err := writeLevels(ctx, tx, name, levels, extras); err != nil {
			return err
		}
		after := PermsFor(levels, extras)
		newPerms[name] = after
		text := "became " + levelsText(levels, extras)
		if lost := lostText(diffPerms(before, after)); lost != "" {
			text += "; no longer allows: " + lost
		}
		changes = append(changes, MigrationChange{Subject: "role " + name, Text: text})
	}

	userChanges, err := migrateUsers(ctx, tx, w)
	if err != nil {
		return err
	}
	changes = append(changes, userChanges...)

	tokenChanges, err := migrateTokens(ctx, tx, w)
	if err != nil {
		return err
	}
	changes = append(changes, tokenChanges...)

	if err := migrateInvites(ctx, tx, w); err != nil {
		return err
	}

	oidcChanges, err := migrateOIDCGroups(ctx, tx)
	if err != nil {
		return err
	}
	changes = append(changes, oidcChanges...)

	if err := raiseMigrationNotice(ctx, tx, changes); err != nil {
		return err
	}

	for _, stmt := range []string{
		`DROP TABLE role_permissions`,
		`DROP TABLE role_scopes`,
		`ALTER TABLE users DROP COLUMN role`,
		`ALTER TABLE users DROP COLUMN scope_tags`,
		`ALTER TABLE oidc_providers DROP COLUMN admin_group`,
		`ALTER TABLE oidc_providers DROP COLUMN operator_group`,
		`ALTER TABLE oidc_providers DROP COLUMN default_scope_tags`,
		`ALTER TABLE invite_tokens DROP COLUMN scope_tags_json`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

func loadLegacyWorld(ctx context.Context, tx *sql.Tx) (*legacyWorld, error) {
	w := &legacyWorld{hostTags: map[string][]string{}, stackHost: map[string]string{}, roles: map[string]*legacyRole{}, labelOfHost: map[string]string{"local": "local"}}
	w.hosts = []string{"local"}
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM agents WHERE status <> 'revoked' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		w.hosts = append(w.hosts, id)
		w.labelOfHost[id] = name
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT host_id, tag FROM host_tags ORDER BY tag`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h, t string
		if err := rows.Scan(&h, &t); err != nil {
			rows.Close()
			return nil, err
		}
		w.hostTags[h] = append(w.hostTags[h], t)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT stack_name, host_id FROM stack_deployments WHERE status <> 'migrated_away'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s, h string
		if err := rows.Scan(&s, &h); err != nil {
			rows.Close()
			return nil, err
		}
		w.stackHost[s] = h
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT name FROM roles ORDER BY builtin DESC, name`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var lr legacyRole
		if err := rows.Scan(&lr.name); err != nil {
			rows.Close()
			return nil, err
		}
		lr.perms = map[Perm]bool{}
		w.roles[lr.name] = &lr
		w.roleOrder = append(w.roleOrder, lr.name)
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT role_name, permission FROM role_permissions`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r, p string
		if err := rows.Scan(&r, &p); err != nil {
			rows.Close()
			return nil, err
		}
		if lr, ok := w.roles[r]; ok {
			lr.perms[Perm(p)] = true
		}
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT role_name, scope_type, scope_value FROM role_scopes ORDER BY role_name, scope_type, scope_value`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r string
		var s legacyScope
		if err := rows.Scan(&r, &s.typ, &s.value); err != nil {
			rows.Close()
			return nil, err
		}
		if lr, ok := w.roles[r]; ok {
			lr.scopes = append(lr.scopes, s)
		}
	}
	rows.Close()
	return w, rows.Err()
}

func countUsersByRole(ctx context.Context, tx *sql.Tx) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT role, COUNT(*) FROM users GROUP BY role`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var r string
		var n int
		if err := rows.Scan(&r, &n); err != nil {
			return nil, err
		}
		out[r] = n
	}
	return out, rows.Err()
}

func parseTags(raw sql.NullString) []string {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw.String), &out); err != nil {
		return nil
	}
	return out
}

func migrateUsers(ctx context.Context, tx *sql.Tx, w *legacyWorld) ([]MigrationChange, error) {
	type urow struct {
		id, name, role, source string
		tags                   []string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, username, role, scope_tags, role_source FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	var users []urow
	for rows.Next() {
		var u urow
		var tags sql.NullString
		if err := rows.Scan(&u.id, &u.name, &u.role, &tags, &u.source); err != nil {
			rows.Close()
			return nil, err
		}
		u.tags = parseTags(tags)
		users = append(users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var changes []MigrationChange
	for _, u := range users {
		lr, ok := w.roles[u.role]
		if !ok {
			if u.role != "" {
				changes = append(changes, MigrationChange{Subject: "user " + u.name,
					Text: fmt.Sprintf("had the role %q, which no longer exists; the account has no access until it gets an assignment", u.role)})
			}
			continue
		}
		scopes, exact := w.scopesFor(lr.scopes, u.tags)
		source := SourceManual
		// Access an identity provider set keeps following its groups only
		// when it was unscoped; otherwise the next sign-in would replace
		// the scoped assignments with the group mapping's global ones.
		if u.source == "sso" {
			if len(scopes) == 1 && scopes[0].IsGlobal() {
				source = SourceIdP
			} else if _, err := tx.ExecContext(ctx, `UPDATE users SET role_source = 'manual' WHERE id = ?`, u.id); err != nil {
				return nil, err
			}
		}
		for _, s := range scopes {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO role_assignments (user_id, role_name, scope_type, scope_host, scope_value, source)
				VALUES (?, ?, ?, ?, ?, ?)`, u.id, u.role, s.Type, s.Host, s.Value, source); err != nil {
				return nil, err
			}
		}
		if len(lr.scopes) == 0 && len(u.tags) == 0 {
			continue
		}
		text := fmt.Sprintf("%s on %s became %s on %s", u.role, legacyScopeText(w, lr.scopes, u.tags), u.role, scopesText(w, scopes))
		if !exact {
			text += " (narrowed: hosts tagged later and stacks that are not deployed are no longer covered)"
		}
		if u.source == "sso" && source == SourceManual {
			text += "; sign-in no longer updates this account's access from identity provider groups"
		}
		changes = append(changes, MigrationChange{Subject: "user " + u.name, Text: text})
	}
	return changes, nil
}

// migrateTokens turns the pinned token role into a restriction. A role
// limited to one scope row keeps that scope; a token whose role had
// several scope rows can't be expressed as one restriction and is revoked.
func migrateTokens(ctx context.Context, tx *sql.Tx, w *legacyWorld) ([]MigrationChange, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, role FROM api_tokens WHERE revoked_at IS NULL`)
	if err != nil {
		return nil, err
	}
	type trow struct {
		id         int64
		name, role string
	}
	var toks []trow
	for rows.Next() {
		var t trow
		if err := rows.Scan(&t.id, &t.name, &t.role); err != nil {
			rows.Close()
			return nil, err
		}
		toks = append(toks, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var changes []MigrationChange
	for _, t := range toks {
		lr, ok := w.roles[t.role]
		if !ok || len(lr.scopes) == 0 {
			continue
		}
		scopes, _ := w.scopesFor(lr.scopes, nil)
		if len(scopes) != 1 {
			if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = CURRENT_TIMESTAMP WHERE id = ?`, t.id); err != nil {
				return nil, err
			}
			changes = append(changes, MigrationChange{Subject: "API token " + t.name,
				Text: fmt.Sprintf("was revoked: its role %s was limited to %s, which a token restriction can't express; create a new token", t.role, legacyScopeText(w, lr.scopes, nil))})
			continue
		}
		s := scopes[0]
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET scope_type = ?, scope_host = ?, scope_value = ? WHERE id = ?`,
			s.Type, s.Host, s.Value, t.id); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

func migrateInvites(ctx context.Context, tx *sql.Tx, w *legacyWorld) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, role, scope_tags_json FROM invite_tokens WHERE used_at IS NULL`)
	if err != nil {
		return err
	}
	type irow struct {
		id   int64
		role string
		tags []string
	}
	var invs []irow
	for rows.Next() {
		var i irow
		var raw sql.NullString
		if err := rows.Scan(&i.id, &i.role, &raw); err != nil {
			rows.Close()
			return err
		}
		i.tags = parseTags(raw)
		invs = append(invs, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, i := range invs {
		var roleScopes []legacyScope
		if lr, ok := w.roles[i.role]; ok {
			roleScopes = lr.scopes
		}
		scopes, _ := w.scopesFor(roleScopes, i.tags)
		if len(scopes) == 0 {
			// Nothing matched both the role's scope and the invite's tags:
			// the invite gave no access and can't be expressed.
			if _, err := tx.ExecContext(ctx, `UPDATE invite_tokens SET scopes_json = '[]', expires_at = CURRENT_TIMESTAMP WHERE id = ?`, i.id); err != nil {
				return err
			}
			continue
		}
		b, err := json.Marshal(scopes)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invite_tokens SET scopes_json = ? WHERE id = ?`, string(b), i.id); err != nil {
			return err
		}
	}
	return nil
}

// MigrationNoticeKey identifies the upgrade notice.
const MigrationNoticeKey = "rbac-assignments-migrated"

func raiseMigrationNotice(ctx context.Context, tx *sql.Tx, changes []MigrationChange) error {
	if len(changes) == 0 {
		return nil
	}
	sort.SliceStable(changes, func(i, j int) bool { return noticeOrder(changes[i].Subject) < noticeOrder(changes[j].Subject) })
	var b strings.Builder
	b.WriteString("Access now comes from assignments of a role on everything, a host, a host tag or a stack. Roles are levels per area plus a few extras. These accounts, roles and tokens changed shape or lost access; review them under Users & roles.")
	for _, c := range changes {
		b.WriteString("\n- " + c.Subject + ": " + c.Text)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO notices (key, audience, audience_value, severity, title, body, link)
		VALUES (?, 'admins', '', 'warning', 'Access changed with the new role model', ?, '/users')
		ON CONFLICT(key) DO UPDATE SET body = excluded.body, resolved_at = NULL`,
		MigrationNoticeKey, b.String())
	return err
}

func noticeOrder(subject string) int {
	switch {
	case strings.HasPrefix(subject, "built-in role"):
		return 0
	case strings.HasPrefix(subject, "role"):
		return 1
	case strings.HasPrefix(subject, "user"):
		return 2
	default:
		return 3
	}
}

// migrateOIDCGroups keeps OIDC sign-in giving what it gave before: it only
// ever read the admin and operator group fields, so those become the
// provider's group mappings and other saved mappings, which never applied,
// are removed and listed for admins to add back on purpose.
func migrateOIDCGroups(ctx context.Context, tx *sql.Tx) ([]MigrationChange, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.display_name, group_concat(m.group_value || ' → ' || m.role_name, ', ')
		  FROM oidc_providers p JOIN oidc_provider_group_mappings m ON m.provider_id = p.id
		 WHERE NOT (m.role_name = 'admin' AND m.group_value = COALESCE(p.admin_group, ''))
		   AND NOT (m.role_name = 'operator' AND m.group_value = COALESCE(p.operator_group, ''))
		 GROUP BY p.id ORDER BY p.display_name`)
	if err != nil {
		return nil, err
	}
	var out []MigrationChange
	for rows.Next() {
		var name, list string
		if err := rows.Scan(&name, &list); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, MigrationChange{Subject: "OIDC provider " + name,
			Text: "its saved group mappings (" + list + ") were never applied at sign-in and were removed; add the ones you want again"})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`DELETE FROM oidc_provider_group_mappings`,
		`INSERT OR IGNORE INTO oidc_provider_group_mappings (provider_id, group_value, role_name)
		 SELECT id, admin_group, 'admin' FROM oidc_providers WHERE COALESCE(admin_group, '') <> ''`,
		`INSERT OR IGNORE INTO oidc_provider_group_mappings (provider_id, group_value, role_name)
		 SELECT id, operator_group, 'operator' FROM oidc_providers WHERE COALESCE(operator_group, '') <> ''`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return nil, err
		}
	}
	return out, nil
}
