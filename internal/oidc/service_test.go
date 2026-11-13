package oidc

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
)

func TestProviderChangeUnlinksAccounts(t *testing.T) {
	in := ProviderInput{Slug: "kc", DisplayName: "Keycloak", IssuerURL: "https://sso.example/realms/main",
		ClientID: "c", ClientSecret: "s", Enabled: true}
	tests := []struct {
		name       string
		issuer     string
		wantLinked bool
	}{
		{name: "same issuer", issuer: "https://sso.example/realms/main", wantLinked: true},
		{name: "trailing slash and case", issuer: "HTTPS://SSO.example/realms/main/", wantLinked: true},
		{name: "other realm", issuer: "https://sso.example/realms/other"},
		{name: "other host", issuer: "https://login.example/realms/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
			s := NewService(database, auth.NewService(database, []byte("0123456789abcdef0123456789abcdef")), nil, "")
			p, err := s.CreateProvider(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO users (id, username, password, oidc_provider, oidc_subject) VALUES ('u1', 'ann', 'x', 'kc', 'sub-1')`); err != nil {
				t.Fatal(err)
			}
			upd := in
			upd.IssuerURL, upd.ClientSecret = tt.issuer, ""
			if _, n, err := s.UpdateProvider(ctx, p.ID, upd); err != nil || (n == 0) != tt.wantLinked {
				t.Fatalf("update unlinked %d, %v; want linked %v", n, err, tt.wantLinked)
			}
			if n, err := s.DeleteProvider(ctx, p.ID); err != nil || (n == 1) != tt.wantLinked {
				t.Fatalf("delete unlinked %d, %v", n, err)
			}
			var linked int
			if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE oidc_provider IS NOT NULL`).Scan(&linked); err != nil {
				t.Fatal(err)
			}
			if linked != 0 {
				t.Error("account still linked after delete")
			}
		})
	}
}

func TestScopesAcceptSpacesAndCommas(t *testing.T) {
	tests := []struct {
		name, in, stored string
		want             []string
	}{
		{"spaces", "openid profile email", "openid profile email", []string{"openid", "profile", "email"}},
		{"commas", "openid,profile,email", "openid profile email", []string{"openid", "profile", "email"}},
		{"mixed", " openid, profile  groups ", "openid profile groups", []string{"openid", "profile", "groups"}},
		{"empty", "", "openid profile email", []string{"openid", "profile", "email"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			if err := db.Migrate(database); err != nil {
				t.Fatal(err)
			}
			s := NewService(database, auth.NewService(database, []byte("0123456789abcdef0123456789abcdef")), nil, "")
			p, err := s.CreateProvider(context.Background(), ProviderInput{Slug: "kc", DisplayName: "Keycloak",
				IssuerURL: "https://sso.example/realms/main", ClientID: "c", ClientSecret: "s", Scopes: tt.in})
			if err != nil {
				t.Fatal(err)
			}
			if p.Scopes != tt.stored {
				t.Errorf("stored %q, want %q", p.Scopes, tt.stored)
			}
			if got := splitScopes(p.Scopes); !slices.Equal(got, tt.want) {
				t.Errorf("split %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRedirectURLFollowsTheBaseURL(t *testing.T) {
	s := NewService(nil, nil, nil, "http://localhost:8080")
	base := "https://dm.lan/"
	s.SetBaseURL(func() string { return base })
	if got := s.redirectURL("kc"); got != "https://dm.lan/api/v1/auth/oidc/kc/callback" {
		t.Fatalf("redirect URL %q", got)
	}
	base = "https://dockmesh.example.com"
	if got := s.redirectURL("kc"); got != "https://dockmesh.example.com/api/v1/auth/oidc/kc/callback" {
		t.Fatalf("after a change: %q", got)
	}
}
