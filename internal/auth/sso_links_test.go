package auth

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dockmesh/dockmesh/internal/rbac"
)

// legacyAccount inserts an OAuth2 account as the upgrade leaves it: no
// subject, the old login-name key and a re-key window.
func legacyAccount(t *testing.T, database *sql.DB, id, name, email, legacy, until string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO users (id, username, email, password, role_source, oauth2_provider, oauth2_legacy_subject, oauth2_rekey_until)
		VALUES (?, ?, ?, 'x', 'sso', 'gh', ?, datetime('now', ?))`, id, name, email, legacy, until); err != nil {
		t.Fatal(err)
	}
}

func TestRekeyLegacy(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, database *sql.DB)
		login   LegacyLogin
		want    string
		subject string // u1's subject afterwards
	}{
		{
			name: "same field moves without email",
			setup: func(t *testing.T, d *sql.DB) {
				legacyAccount(t, d, "u1", "alice", "alice@example.com", "1.2345678e+07", "+30 days")
			},
			login:   LegacyLogin{Subject: "12345678", Legacy: "1.2345678e+07", SameSource: true, Email: "new@example.com"},
			want:    "u1",
			subject: "12345678",
		},
		{
			name: "login name with matching email",
			setup: func(t *testing.T, d *sql.DB) {
				legacyAccount(t, d, "u1", "alice", "alice@example.com", "alice", "+30 days")
			},
			login:   LegacyLogin{Subject: "1001", Legacy: "alice", Email: "ALICE@example.com"},
			want:    "u1",
			subject: "1001",
		},
		{
			name: "reused login name with another email",
			setup: func(t *testing.T, d *sql.DB) {
				legacyAccount(t, d, "u1", "alice", "alice@example.com", "alice", "+30 days")
			},
			login: LegacyLogin{Subject: "2002", Legacy: "alice", Email: "mallory@example.com"},
		},
		{
			name: "window ended",
			setup: func(t *testing.T, d *sql.DB) {
				legacyAccount(t, d, "u1", "alice", "alice@example.com", "alice", "-1 days")
			},
			login: LegacyLogin{Subject: "1001", Legacy: "alice", Email: "alice@example.com"},
		},
		{
			name: "subject already taken",
			setup: func(t *testing.T, d *sql.DB) {
				legacyAccount(t, d, "u1", "alice", "alice@example.com", "alice", "+30 days")
				if _, err := d.Exec(`INSERT INTO users (id, username, password, oauth2_provider, oauth2_subject) VALUES ('u2', 'alice2', 'x', 'gh', '1001')`); err != nil {
					t.Fatal(err)
				}
			},
			login: LegacyLogin{Subject: "1001", Legacy: "alice", Email: "alice@example.com"},
		},
		{
			name: "two accounts with the key",
			setup: func(t *testing.T, d *sql.DB) {
				legacyAccount(t, d, "u1", "alice", "alice@example.com", "alice", "+30 days")
				legacyAccount(t, d, "u3", "alice-old", "old@example.com", "alice", "+30 days")
			},
			login: LegacyLogin{Subject: "1001", Legacy: "alice", SameSource: true, Email: "alice@example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, database := newTestService(t)
			tt.setup(t, database)
			tt.login.Kind, tt.login.Provider = "oauth2", "gh"
			got, err := s.RekeyLegacy(ctx, tt.login)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("rekeyed %q, want %q", got, tt.want)
			}
			var subject sql.NullString
			if err := database.QueryRow(`SELECT oauth2_subject FROM users WHERE id = 'u1'`).Scan(&subject); err != nil {
				t.Fatal(err)
			}
			if subject.String != tt.subject {
				t.Errorf("u1 subject = %q, want %q", subject.String, tt.subject)
			}
		})
	}
}

// A renamed login taken over by someone else signs in neither to the old
// account during the re-key window nor after it.
func TestLegacyAccountNotTakenOver(t *testing.T) {
	ctx := context.Background()
	s, database := newTestService(t)
	s.SetSettings(mapSettings{})
	store := rbac.NewStore(database)
	s.SetAccessHooks(func(ctx context.Context) {
		if err := store.Load(ctx); err != nil {
			t.Error(err)
		}
	}, store.ReplaceIdPAssignments)
	if _, err := database.Exec(`INSERT INTO oauth2_providers (slug, display_name, authorization_url, token_url, userinfo_url, client_id, client_secret)
		VALUES ('gh', 'GitHub', 'a', 't', 'https://api.github.com/user', 'c', 's')`); err != nil {
		t.Fatal(err)
	}
	legacyAccount(t, database, "victim", "alice", "alice@example.com", "alice", "+30 days")

	attacker := LegacyLogin{Kind: "oauth2", Provider: "gh", Subject: "2002", Legacy: "alice", Email: "mallory@example.com"}
	if id, err := s.RekeyLegacy(ctx, attacker); err != nil || id != "" {
		t.Fatalf("rekey = %q, %v; want nothing", id, err)
	}
	u, err := s.ProvisionSSOUser(ctx, SSOIdentity{Kind: "oauth2", Provider: "gh", Subject: "2002", Email: "mallory@example.com", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == "victim" {
		t.Fatal("attacker signed into the renamed user's account")
	}

	// The legacy key itself never matches as a subject.
	if _, err := database.Exec(`UPDATE users SET oauth2_rekey_until = NULL WHERE id = 'victim'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProvisionSSOUser(ctx, SSOIdentity{Kind: "oauth2", Provider: "gh", Subject: "alice", Email: "alice@example.com"}); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("legacy key as subject: %v, want ErrSSOEmailInUse", err)
	}
}

func TestUnlinkProvider(t *testing.T) {
	ctx := context.Background()
	s, database := newTestService(t)
	if _, err := database.Exec(`INSERT INTO users (id, username, password, ldap_provider, ldap_subject) VALUES
		('a', 'a', 'x', 'corp', 'guid-a'), ('b', 'b', 'x', 'other', 'guid-b')`); err != nil {
		t.Fatal(err)
	}
	legacyAccount(t, database, "c", "c", "c@example.com", "c", "+30 days")
	n, err := UnlinkProvider(ctx, database, "ldap", "corp")
	if err != nil || n != 1 {
		t.Fatalf("unlinked %d, %v; want 1", n, err)
	}
	if n, err := UnlinkProvider(ctx, database, "oauth2", "gh"); err != nil || n != 1 {
		t.Fatalf("unlinked %d, %v; want 1", n, err)
	}
	var linked int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE ldap_provider IS NOT NULL OR oauth2_provider IS NOT NULL
		OR oauth2_legacy_subject IS NOT NULL`).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 1 {
		t.Errorf("%d linked accounts left, want only the other provider's", linked)
	}
	if _, err := UnlinkProvider(ctx, database, "bogus", "x"); err == nil {
		t.Error("unknown kind accepted")
	}
	p, err := s.PendingSSORekeys(ctx)
	if err != nil || len(p.Accounts) != 0 {
		t.Errorf("pending = %+v, %v; want none", p, err)
	}
}

func TestPendingAndEndSSORekey(t *testing.T) {
	ctx := context.Background()
	s, database := newTestService(t)
	legacyAccount(t, database, "u1", "ann", "ann@example.com", "ann", "+30 days")
	legacyAccount(t, database, "u2", "old", "old@example.com", "old", "-1 days")
	if _, err := database.Exec(`INSERT INTO users (id, username, password, ldap_provider, ldap_legacy_subject, ldap_rekey_until)
		VALUES ('u3', 'bob', 'x', 'corp', 'uid=bob,dc=x', datetime('now', '+30 days'))`); err != nil {
		t.Fatal(err)
	}
	p, err := s.PendingSSORekeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Accounts) != 2 || p.Accounts[0] != "ann" || p.Accounts[1] != "bob" || p.Until == "" {
		t.Fatalf("pending = %+v, want ann and bob", p)
	}
	n, err := s.EndSSORekey(ctx)
	if err != nil || n != 2 {
		t.Fatalf("ended %d, %v; want 2", n, err)
	}
	if p, _ := s.PendingSSORekeys(ctx); len(p.Accounts) != 0 {
		t.Errorf("pending after end = %v", p.Accounts)
	}
}
