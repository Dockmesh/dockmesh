package clusters

import (
	"context"
	"errors"
	"testing"
)

func seedAccess(t *testing.T, s *Service) {
	t.Helper()
	stmts := []string{
		`INSERT INTO users (id, username, password) VALUES ('u1', 'alice', 'x'), ('u2', 'bob', 'x')`,
		`INSERT INTO role_assignments (user_id, role_name, scope_type, scope_host, scope_value, source, via) VALUES
			('u1', 'viewer',   'cluster', '` + swarmID + `', '', 'manual', ''),
			('u2', 'operator', 'cluster', '` + swarmID + `', '', 'idp',    'ops'),
			('u1', 'viewer',   'cluster', 'other',          '', 'manual', ''),
			('u2', 'viewer',   'host',    '` + swarmID + `', '', 'manual', '')`,
		`INSERT INTO oidc_providers (id, slug, display_name, issuer_url, client_id, client_secret, default_role) VALUES (1, 'corp', 'Corp', 'https://idp.test', 'c', 's', 'viewer')`,
		`INSERT INTO saml_providers (id, slug, display_name, entity_id, sso_url, idp_metadata_xml, idp_cert_pem) VALUES (1, 'sam', 'Sam', 'e', 'https://s', 'x', 'x')`,
		`INSERT INTO ldap_providers (id, slug, display_name, host, user_search_base) VALUES (1, 'dir', 'Dir', 'ldap.test', 'dc=x')`,
		`INSERT INTO oauth2_providers (id, slug, display_name, authorization_url, token_url, userinfo_url, client_id, client_secret) VALUES (1, 'gh', 'GH', 'https://a', 'https://t', 'https://u', 'c', 's')`,
		`INSERT INTO oidc_provider_group_mappings (provider_id, group_value, role_name, scope_type, scope_host) VALUES (1, 'ops', 'operator', 'cluster', '` + swarmID + `'), (1, 'ops', 'viewer', 'cluster', 'other')`,
		`INSERT INTO saml_provider_group_mappings (provider_id, group_value, role_name, scope_type, scope_host) VALUES (1, 'sg', 'viewer', 'cluster', '` + swarmID + `')`,
		`INSERT INTO ldap_provider_group_mappings (provider_id, group_value, role_name, scope_type, scope_host) VALUES (1, 'lg', 'viewer', 'cluster', '` + swarmID + `')`,
		`INSERT INTO oauth2_provider_group_mappings (provider_id, group_value, role_name, scope_type, scope_host) VALUES (1, 'og', 'viewer', 'host', 'm1')`,
		`INSERT INTO api_tokens (id, token_prefix, token_hash, name, role, created_by_user_id, scope_type, scope_host) VALUES
			(1, 'dmt_a', 'h1', 'ci',      'deployer', 'u1', 'cluster', '` + swarmID + `'),
			(2, 'dmt_b', 'h2', 'scrape',  'viewer',   'u2', 'cluster', 'other'),
			(3, 'dmt_c', 'h3', 'all',     '',         'u1', 'global',  ''),
			(4, 'dmt_d', 'h4', 'host',    'viewer',   'u1', 'host',    '` + swarmID + `')`,
		`INSERT INTO invite_tokens (id, token_hash, role, created_by, expires_at, scopes_json, email_hint) VALUES
			(1, 'i1', 'viewer', 'u1', '2099-01-01 00:00:00', '[{"type":"cluster","host":"` + swarmID + `"}]', 'only@x'),
			(2, 'i2', 'viewer', 'u1', '2099-01-01 00:00:00', '[{"type":"cluster","host":"` + swarmID + `"},{"type":"host","host":"m1"}]', 'mixed@x'),
			(3, 'i3', 'viewer', 'u1', '2099-01-01 00:00:00', '[{"type":"cluster","host":"other"}]', 'other@x'),
			(4, 'i4', 'viewer', 'u1', '2099-01-01 00:00:00', '[{"type":"global"}]', 'global@x')`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// Deleting a cluster removes every assignment, mapping and invite scope
// on it, revokes the tokens restricted to it and leaves other scopes and
// clusters alone.
func TestDeleteRemovesClusterAccess(t *testing.T) {
	ctx := context.Background()
	s := newService(t, lab())
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	seedAccess(t, s)

	pre, err := s.AccessOf(ctx, swarmID)
	if err != nil {
		t.Fatal(err)
	}
	_, removed, err := s.Delete(ctx, swarmID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"assignments": 2, "invites": 2, "mappings": 3, "tokens": 1}
	for k, n := range want {
		if pre.Counts()[k] != n || removed.Counts()[k] != n {
			t.Errorf("%s: preview %d, removed %d, want %d", k, pre.Counts()[k], removed.Counts()[k], n)
		}
	}
	if removed.Tokens[0].Name != "ci" || removed.Tokens[0].Owner != "alice" {
		t.Errorf("token %+v", removed.Tokens[0])
	}
	for _, inv := range removed.Invites {
		if inv.Expired != (inv.EmailHint == "only@x") {
			t.Errorf("invite %+v", inv)
		}
	}

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"assignments left", `SELECT group_concat(scope_type || ':' || scope_host, ',') FROM (SELECT * FROM role_assignments ORDER BY id)`, "cluster:other,host:" + swarmID},
		{"mappings left", `SELECT (SELECT COUNT(*) FROM oidc_provider_group_mappings WHERE scope_host = 'other') || '/' ||
			((SELECT COUNT(*) FROM oidc_provider_group_mappings) + (SELECT COUNT(*) FROM saml_provider_group_mappings) +
			(SELECT COUNT(*) FROM ldap_provider_group_mappings) + (SELECT COUNT(*) FROM oauth2_provider_group_mappings))`, "1/2"},
		{"cluster token revoked, restriction kept", `SELECT (revoked_at IS NOT NULL) || role || scope_type || scope_host FROM api_tokens WHERE id = 1`, "1deployercluster" + swarmID},
		{"other tokens active", `SELECT COUNT(*) FROM api_tokens WHERE revoked_at IS NULL AND id IN (2, 3, 4)`, "3"},
		{"cluster-only invite expired", `SELECT scopes_json || (expires_at <= CURRENT_TIMESTAMP) FROM invite_tokens WHERE id = 1`, "[]1"},
		{"mixed invite narrowed", `SELECT scopes_json || (expires_at > CURRENT_TIMESTAMP) FROM invite_tokens WHERE id = 2`, `[{"type":"host","host":"m1"}]1`},
		{"other invites untouched", `SELECT group_concat(scopes_json, ';') FROM (SELECT scopes_json FROM invite_tokens WHERE id IN (3, 4) ORDER BY id)`, `[{"type":"cluster","host":"other"}];[{"type":"global"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			if err := s.db.QueryRow(tt.query).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}

	if _, err := s.AccessOf(ctx, swarmID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("access of deleted cluster: %v", err)
	}
}

// A failing step leaves the cluster and its access as they were.
func TestDeleteIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := newService(t, lab())
	if _, err := s.Create(ctx, "m1", "prod", ""); err != nil {
		t.Fatal(err)
	}
	seedAccess(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON clusters BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Delete(ctx, swarmID); err == nil {
		t.Fatal("delete succeeded")
	}
	a, err := s.AccessOf(ctx, swarmID)
	if err != nil {
		t.Fatal(err)
	}
	if c := a.Counts(); c["assignments"] != 2 || c["tokens"] != 1 || c["mappings"] != 3 || c["invites"] != 2 {
		t.Fatalf("access after failed delete: %v", c)
	}
}
