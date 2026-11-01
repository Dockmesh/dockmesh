package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// legacyUser is an account as the old model saw it.
type legacyUser struct {
	id, role string
	tags     []string
	sso      bool
}

type legacyFixture struct {
	rolePerms  map[string][]string
	roleScopes map[string][][2]string // role → (type, value)
	users      []legacyUser
}

var migrationFixture = legacyFixture{
	rolePerms: map[string][]string{
		"ops-custom":   {"containers.view", "containers.logs", "containers.update", "containers.exec", "stacks.view"},
		"odd":          {"users.view", "backups.restore", "proxy.view"},
		"scoped-hosts": {"stacks.view", "containers.view", "containers.logs"},
		"stack-role":   {"stacks.view", "stacks.deploy", "stacks.update", "containers.view", "containers.logs"},
		"one-host":     {"containers.view", "containers.logs"},
	},
	roleScopes: map[string][][2]string{
		"scoped-hosts": {{"host", "agent-1"}, {"host_tag", "dev"}},
		"stack-role":   {{"stack", "web"}, {"stack", "draft"}},
		"one-host":     {{"host", "agent-2"}},
	},
	users: []legacyUser{
		{id: "u-admin", role: "admin"},
		{id: "u-op", role: "operator"},
		{id: "u-tags", role: "viewer", tags: []string{"prod"}},
		{id: "u-custom", role: "ops-custom"},
		{id: "u-odd", role: "odd"},
		{id: "u-rolescoped", role: "scoped-hosts"},
		{id: "u-both", role: "scoped-hosts", tags: []string{"prod"}},
		{id: "u-stack", role: "stack-role"},
		{id: "u-sso", role: "operator", sso: true},
		{id: "u-sso-tags", role: "viewer", tags: []string{"dev"}, sso: true},
	},
}

func seedLegacy(t *testing.T, database *sql.DB, f legacyFixture) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO agents (id, name, status) VALUES ('agent-1', 'edge1', 'online'), ('agent-2', 'edge2', 'online')`)
	exec(`INSERT INTO host_tags (host_id, tag) VALUES ('agent-1', 'prod'), ('agent-2', 'dev')`)
	exec(`INSERT INTO stack_deployments (stack_name, host_id) VALUES ('web', 'agent-1'), ('db', 'agent-2')`)
	for role, perms := range f.rolePerms {
		exec(`INSERT INTO roles (name, display, builtin) VALUES (?, ?, 0)`, role, role)
		for _, p := range perms {
			exec(`INSERT INTO role_permissions (role_name, permission) VALUES (?, ?)`, role, p)
		}
		for _, sc := range f.roleScopes[role] {
			exec(`INSERT INTO role_scopes (role_name, scope_type, scope_value) VALUES (?, ?, ?)`, role, sc[0], sc[1])
		}
	}
	exec(`INSERT INTO oidc_providers (id, slug, display_name, issuer_url, client_id, client_secret, admin_group, default_role)
		VALUES (1, 'corp', 'Corp', 'https://idp.test', 'c', 's', 'admins', 'viewer')`)
	exec(`INSERT INTO oidc_provider_group_mappings (provider_id, group_value, role_name) VALUES (1, 'devs', 'deployer')`)
	for _, u := range f.users {
		var tags any
		if len(u.tags) > 0 {
			b, _ := json.Marshal(u.tags)
			tags = string(b)
		}
		source, provider := "manual", any(nil)
		if u.sso {
			source, provider = "sso", "corp"
		}
		exec(`INSERT INTO users (id, username, password, role, scope_tags, role_source, oidc_provider, oidc_subject) VALUES (?, ?, 'x', ?, ?, ?, ?, ?)`,
			u.id, u.id, u.role, tags, source, provider, u.id)
	}
	exec(`INSERT INTO api_tokens (token_prefix, token_hash, name, role, created_by_user_id) VALUES
		('dmt_aaaaaaaa', 'h1', 'plain', 'viewer', 'u-admin'),
		('dmt_bbbbbbbb', 'h2', 'two-scopes', 'scoped-hosts', 'u-admin'),
		('dmt_cccccccc', 'h3', 'one-scope', 'one-host', 'u-admin')`)
	exec(`INSERT INTO invite_tokens (token_hash, role, scope_tags_json, expires_at, created_by) VALUES
		('i1', 'viewer', '["prod"]', datetime('now', '+1 day'), 'u-admin'),
		('i2', 'one-host', '["prod"]', datetime('now', '+1 day'), 'u-admin')`)
}

// oldAllows evaluates the old model: role permissions, within the role's
// scope rows (any) and the user's tags (any).
func oldAllows(f legacyFixture, u legacyUser, builtin map[string]map[rbac.Perm]bool, p rbac.Perm, res rbac.Resource) bool {
	var perms map[rbac.Perm]bool
	if b, ok := builtin[u.role]; ok {
		perms = b
	} else {
		old := map[rbac.Perm]bool{}
		for _, q := range f.rolePerms[u.role] {
			old[rbac.Perm(q)] = true
		}
		perms = rbac.LegacyEffective(old)
	}
	if !perms[p] && !rbac.IsBase(p) {
		return false
	}
	if len(u.tags) > 0 && (res.HostID == "" || !slices.ContainsFunc(res.HostTags, func(t string) bool { return slices.Contains(u.tags, t) })) {
		return false
	}
	scopes := f.roleScopes[u.role]
	if len(scopes) == 0 {
		return true
	}
	for _, sc := range scopes {
		switch sc[0] {
		case "host":
			if res.HostID == sc[1] {
				return true
			}
		case "host_tag":
			if slices.Contains(res.HostTags, sc[1]) {
				return true
			}
		case "stack":
			if res.StackName == sc[1] {
				return true
			}
		}
	}
	return false
}

func TestRBACAssignmentsMigration(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, lastLegacyRBAC); err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, database, migrationFixture)
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := rbac.NewStore(database)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}

	scopes := func(user string) string {
		var out []string
		for _, a := range store.UserAssignments(user) {
			out = append(out, a.Role+"@"+a.Scope.String()+"/"+a.Source)
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	for user, want := range map[string]string{
		"u-admin":      "admin@everything/manual",
		"u-tags":       "viewer@host tag prod/manual",
		"u-rolescoped": "scoped-hosts@host agent-1/manual, scoped-hosts@host tag dev/manual",
		"u-both":       "scoped-hosts@host agent-1/manual",
		"u-stack":      "stack-role@stack draft/manual, stack-role@stack web on agent-1/manual",
		"u-sso":        "operator@everything/idp",
		"u-sso-tags":   "viewer@host tag dev/manual",
	} {
		if got := scopes(user); got != want {
			t.Errorf("%s: %s, want %s", user, got, want)
		}
	}
	var source string
	if err := database.QueryRow(`SELECT role_source FROM users WHERE id = 'u-sso-tags'`).Scan(&source); err != nil || source != "manual" {
		t.Errorf("scoped SSO account keeps syncing from groups: %q %v", source, err)
	}

	role := func(name string) rbac.Role {
		r, ok := store.Role(name)
		if !ok {
			t.Fatalf("role %s missing", name)
		}
		return r
	}
	custom := role("ops-custom")
	// containers.update used to cover removing and updating containers.
	if custom.Builtin || custom.Levels["containers"] != "manage" || custom.Levels["stacks"] != "view" ||
		!slices.Contains(custom.Extras, "terminal") || !slices.Contains(custom.Extras, "env") || slices.Contains(custom.Extras, "files") {
		t.Errorf("ops-custom = %+v", custom)
	}
	odd := role("odd")
	for _, a := range rbac.Areas {
		want := rbac.LevelNone
		if a.Key == "proxy" {
			want = "view"
		}
		if odd.Levels[a.Key] != want {
			t.Errorf("odd: %s = %s, want %s", a.Key, odd.Levels[a.Key], want)
		}
	}
	if !role(rbac.RoleOperator).Builtin {
		t.Error("operator lost builtin")
	}

	// Never wider than before, for every account, permission and place.
	builtin := map[string]map[rbac.Perm]bool{}
	for _, b := range rbac.BuiltinRoles {
		builtin[b.Name] = rbac.PermsFor(b.Levels, b.Extras)
	}
	hosts := map[string][]string{"local": nil, "agent-1": {"prod"}, "agent-2": {"dev"}}
	stacks := map[string]string{"web": "agent-1", "db": "agent-2", "draft": "", "": ""}
	var places []rbac.Resource
	for h, tags := range hosts {
		places = append(places, rbac.Resource{HostID: h, HostTags: tags})
	}
	for s, h := range stacks {
		if s != "" {
			places = append(places, rbac.Resource{HostID: h, HostTags: hosts[h], StackName: s})
		}
	}
	for _, u := range migrationFixture.users {
		acc := store.AccessFor(u.id)
		for _, p := range rbac.AllPerms() {
			if rbac.IsBase(p) {
				continue
			}
			for _, res := range places {
				if acc.AllowsOn(p, res) && !oldAllows(migrationFixture, u, builtin, p, res) {
					t.Errorf("%s gained %s on %+v", u.id, p, res)
				}
			}
		}
	}

	var plain, two, one string
	if err := database.QueryRow(`SELECT scope_type || ':' || scope_host FROM api_tokens WHERE name = 'one-scope'`).Scan(&one); err != nil || one != "host:agent-2" {
		t.Errorf("one-scope token restriction %q %v", one, err)
	}
	if err := database.QueryRow(`SELECT scope_type FROM api_tokens WHERE name = 'plain'`).Scan(&plain); err != nil || plain != "global" {
		t.Errorf("plain token restriction %q %v", plain, err)
	}
	if err := database.QueryRow(`SELECT COALESCE(revoked_at, '') FROM api_tokens WHERE name = 'two-scopes'`).Scan(&two); err != nil || two == "" {
		t.Errorf("token with two role scopes not revoked: %q %v", two, err)
	}
	var inviteScopes string
	if err := database.QueryRow(`SELECT scopes_json FROM invite_tokens WHERE token_hash = 'i1'`).Scan(&inviteScopes); err != nil || inviteScopes != `[{"type":"host_tag","value":"prod"}]` {
		t.Errorf("invite scopes %s %v", inviteScopes, err)
	}
	// one-host is limited to agent-2, which isn't tagged prod: no access.
	var live int
	if err := database.QueryRow(`SELECT COUNT(*) FROM invite_tokens WHERE token_hash = 'i2' AND scopes_json = '[]' AND expires_at <= CURRENT_TIMESTAMP`).Scan(&live); err != nil || live != 1 {
		t.Errorf("invite without access not expired: %d %v", live, err)
	}
	var mappings string
	if err := database.QueryRow(`SELECT group_concat(group_value || '=' || role_name, ',') FROM (SELECT * FROM oidc_provider_group_mappings ORDER BY group_value)`).Scan(&mappings); err != nil || mappings != "admins=admin" {
		t.Errorf("oidc mappings %q %v", mappings, err)
	}

	for _, gone := range []string{
		`SELECT role FROM users`, `SELECT scope_tags FROM users`, `SELECT 1 FROM role_permissions`, `SELECT 1 FROM role_scopes`,
		`SELECT admin_group FROM oidc_providers`, `SELECT scope_tags_json FROM invite_tokens`,
	} {
		if _, err := database.Exec(gone); err == nil {
			t.Errorf("still present after the migration: %s", gone)
		}
	}

	var body string
	if err := database.QueryRow(`SELECT body FROM notices WHERE key = ? AND audience = 'admins'`, rbac.MigrationNoticeKey).Scan(&body); err != nil {
		t.Fatalf("migration notice: %v", err)
	}
	for _, want := range []string{
		"- built-in role operator: now follows the new definition and no longer allows:",
		"- role odd: became Proxy view; no longer allows: Backups (backups.restore); Users & roles (users.view)",
		"- role ops-custom: became Stacks view, Containers manage, extra terminal, extra env; no longer allows: extra files (containers.files)",
		"- user u-both: scoped-hosts on host edge1 or host tag dev, limited to hosts tagged prod became scoped-hosts on host edge1 (narrowed",
		"- user u-stack: stack-role on stack draft or stack web became stack-role on stack draft, stack web on edge1",
		"- user u-sso-tags: viewer on everything, limited to hosts tagged dev became viewer on host tag dev; sign-in no longer updates",
		"- API token two-scopes: was revoked",
		"- OIDC provider Corp: its saved group mappings (devs → deployer) were never applied at sign-in and were removed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("notice lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "user u-admin") || strings.Contains(body, "built-in role admin") {
		t.Errorf("unchanged access listed:\n%s", body)
	}
}

// A database with nothing to migrate gets no notice and keeps the
// built-in roles.
func TestRBACAssignmentsMigrationFreshInstall(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM roles WHERE builtin = 1`).Scan(&n); err != nil || n != len(rbac.BuiltinRoles) {
		t.Errorf("built-in roles = %d (%v), want %d", n, err, len(rbac.BuiltinRoles))
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM notices WHERE key = ?`, rbac.MigrationNoticeKey).Scan(&n); err != nil || n != 0 {
		t.Errorf("fresh install notice count %d %v", n, err)
	}
}

// A failing data step leaves the old schema in place.
func TestRBACAssignmentsMigrationRollsBack(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, lastLegacyRBAC); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users (id, username, password, role) VALUES ('u1', 'u1', 'x', 'admin')`); err != nil {
		t.Fatal(err)
	}
	name := "20261101151453_rbac_assignments.sql"
	prev := dataSteps[name]
	dataSteps[name] = func(ctx context.Context, tx *sql.Tx) error {
		if err := prev(ctx, tx); err != nil {
			return err
		}
		return sql.ErrTxDone
	}
	t.Cleanup(func() { dataSteps[name] = prev })
	if err := Migrate(database); err == nil {
		t.Fatal("migration with a failing data step succeeded")
	}
	var role string
	if err := database.QueryRow(`SELECT role FROM users WHERE id = 'u1'`).Scan(&role); err != nil || role != "admin" {
		t.Errorf("old schema not intact: %q %v", role, err)
	}
	if _, err := database.Exec(`SELECT 1 FROM role_assignments`); err == nil {
		t.Error("new table survived the rollback")
	}
}
