package oauth2auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
)

func TestSubject(t *testing.T) {
	github := map[string]any{"id": float64(12345678), "login": "alice", "email": "a@example.com"}
	gitea := map[string]any{"sub": "7", "id": float64(7), "preferred_username": "alice"}
	tests := []struct {
		name      string
		field     string
		claims    map[string]any
		want      string
		wantField string
		wantErr   bool
	}{
		{name: "numeric id without exponent", claims: github, want: "12345678", wantField: "id"},
		{name: "sub before id", claims: gitea, want: "7", wantField: "sub"},
		{name: "configured field", field: "login", claims: github, want: "alice", wantField: "login"},
		{name: "configured field missing", field: "uuid", claims: github, wantErr: true},
		{name: "no id at all", claims: map[string]any{"login": "alice"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, field, err := Subject(tt.field, tt.claims)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want || field != tt.wantField {
				t.Errorf("got %q from %q, want %q from %q", got, field, tt.want, tt.wantField)
			}
		})
	}
}

func TestLegacySubject(t *testing.T) {
	claims := map[string]any{"id": float64(12345678), "login": "alice", "sub": "s-1"}
	tests := []struct {
		usernameField, want, wantField string
	}{
		{"login", "alice", "login"},
		{"id", "1.2345678e+07", "id"},
		{"missing", "s-1", "sub"},
	}
	for _, tt := range tests {
		got, field := LegacySubject(tt.usernameField, claims)
		if got != tt.want || field != tt.wantField {
			t.Errorf("LegacySubject(%q) = %q, %q; want %q, %q", tt.usernameField, got, field, tt.want, tt.wantField)
		}
	}
}

func TestIdentityKey(t *testing.T) {
	base := IdentityKey("https://api.github.com/user", "")
	tests := []struct {
		url, field string
		same       bool
	}{
		{"https://API.github.com/user/", "", true},
		{"https://api.github.com/v3/user", "", true},
		{"https://git.example.com/api/v1/user", "", false},
		{"https://api.github.com/user", "login", false},
	}
	for _, tt := range tests {
		if got := IdentityKey(tt.url, tt.field) == base; got != tt.same {
			t.Errorf("IdentityKey(%q, %q) same = %v, want %v", tt.url, tt.field, got, tt.same)
		}
	}
}

func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return NewService(database, auth.NewService(database, []byte("0123456789abcdef0123456789abcdef")), nil, ""), database
}

func linkedCount(t *testing.T, database *sql.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE oauth2_provider IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Accounts follow their provider only while it is the same identity
// provider; a different one, a delete or a recreated slug detaches them.
func TestProviderChangeUnlinksAccounts(t *testing.T) {
	in := ProviderInput{Slug: "gh", DisplayName: "GitHub", AuthorizationURL: "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token", UserinfoURL: "https://api.github.com/user",
		ClientID: "c", ClientSecret: "s", Enabled: true}
	tests := []struct {
		name       string
		change     func(in ProviderInput) ProviderInput
		wantLinked bool
	}{
		{name: "display name", wantLinked: true, change: func(in ProviderInput) ProviderInput { in.DisplayName = "GH"; return in }},
		{name: "userinfo host", change: func(in ProviderInput) ProviderInput {
			in.UserinfoURL = "https://git.example.com/api/v1/user"
			return in
		}},
		{name: "subject field", change: func(in ProviderInput) ProviderInput { in.SubjectField = "login"; return in }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, database := newTestService(t)
			p, err := s.CreateProvider(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO users (id, username, password, oauth2_provider, oauth2_subject) VALUES ('u1', 'ann', 'x', 'gh', '1')`); err != nil {
				t.Fatal(err)
			}
			upd := tt.change(in)
			upd.ClientSecret = ""
			_, n, err := s.UpdateProvider(ctx, p.ID, upd)
			if err != nil {
				t.Fatal(err)
			}
			if got := linkedCount(t, database) == 1; got != tt.wantLinked || (n == 0) != tt.wantLinked {
				t.Errorf("linked = %v (unlinked %d), want linked %v", got, n, tt.wantLinked)
			}
		})
	}

	t.Run("delete and recreate", func(t *testing.T) {
		ctx := context.Background()
		s, database := newTestService(t)
		p, err := s.CreateProvider(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO users (id, username, password, oauth2_provider, oauth2_subject) VALUES ('u1', 'ann', 'x', 'gh', '1')`); err != nil {
			t.Fatal(err)
		}
		if n, err := s.DeleteProvider(ctx, p.ID); err != nil || n != 1 {
			t.Fatalf("delete unlinked %d, %v; want 1", n, err)
		}
		if linkedCount(t, database) != 0 {
			t.Fatal("account still linked after delete")
		}
		// A link left behind by older versions is dropped when the slug comes back.
		if _, err := database.Exec(`UPDATE users SET oauth2_provider = 'gh', oauth2_subject = '1' WHERE id = 'u1'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateProvider(ctx, in); err != nil {
			t.Fatal(err)
		}
		if linkedCount(t, database) != 0 {
			t.Fatal("recreated slug picked up the old account")
		}
	})
}
