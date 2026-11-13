package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSSOImmutableSubjectsMigration(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrateUpTo(database, "20261102125620_deploy_history_environment.sql"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO oauth2_providers (slug, display_name, authorization_url, token_url, userinfo_url, client_id, client_secret)
		 VALUES ('gh', 'GitHub', 'a', 't', 'https://api.github.com/user', 'c', 's')`,
		`INSERT INTO ldap_providers (slug, display_name, host, user_search_base) VALUES ('corp', 'Corp', 'dc1', 'dc=corp')`,
		`INSERT INTO oidc_providers (slug, display_name, issuer_url, client_id, client_secret) VALUES ('kc', 'KC', 'https://kc', 'c', 's')`,
		`INSERT INTO users (id, username, password, oauth2_provider, oauth2_subject) VALUES
		 ('o1', 'ann', 'x', 'gh', 'ann'), ('o2', 'gone', 'x', 'deleted', '7')`,
		`INSERT INTO users (id, username, password, ldap_provider, ldap_subject) VALUES ('l1', 'bob', 'x', 'corp', 'uid=bob,dc=corp')`,
		`INSERT INTO users (id, username, password, oidc_provider, oidc_subject) VALUES
		 ('i1', 'cid', 'x', 'kc', 'sub-1'), ('i2', 'dan', 'x', 'old', 'sub-2')`,
		`INSERT INTO users (id, username, password, saml_provider, saml_subject) VALUES ('s1', 'eve', 'x', 'nope', 'n-1')`,
	} {
		if _, err := database.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id, query string
		want      string
	}{
		{"o1", `SELECT COALESCE(oauth2_subject, '-') || '|' || oauth2_legacy_subject || '|' ||
			(oauth2_rekey_until BETWEEN datetime('now', '+29 days') AND datetime('now', '+31 days')) FROM users WHERE id = ?`, "-|ann|1"},
		{"l1", `SELECT COALESCE(ldap_subject, '-') || '|' || ldap_legacy_subject || '|' ||
			(ldap_rekey_until BETWEEN datetime('now', '+29 days') AND datetime('now', '+31 days')) FROM users WHERE id = ?`, "-|uid=bob,dc=corp|1"},
		{"o2", `SELECT COALESCE(oauth2_provider, '-') || COALESCE(oauth2_legacy_subject, '-') FROM users WHERE id = ?`, "--"},
		{"i1", `SELECT oidc_provider || '|' || oidc_subject FROM users WHERE id = ?`, "kc|sub-1"},
		{"i2", `SELECT COALESCE(oidc_provider, '-') || COALESCE(oidc_subject, '-') FROM users WHERE id = ?`, "--"},
		{"s1", `SELECT COALESCE(saml_provider, '-') || COALESCE(saml_subject, '-') FROM users WHERE id = ?`, "--"},
	}
	for _, tt := range tests {
		var got sql.NullString
		if err := database.QueryRow(tt.query, tt.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got.String != tt.want {
			t.Errorf("%s: got %q, want %q", tt.id, got.String, tt.want)
		}
	}
}
