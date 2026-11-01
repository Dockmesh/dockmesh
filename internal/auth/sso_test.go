package auth

import (
	"context"
	"errors"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"strings"
	"testing"
)

type mapSettings map[string]string

func (m mapSettings) Get(key, def string) string {
	if v, ok := m[key]; ok {
		return v
	}
	return def
}

func (m mapSettings) Set(_ context.Context, key, value string) error {
	m[key] = value
	return nil
}

func TestProvisionSSOUser(t *testing.T) {
	ctx := context.Background()
	s, database := newTestService(t)
	settings := mapSettings{}
	s.SetSettings(settings)
	if _, err := database.Exec(`INSERT INTO oidc_providers (id, slug, display_name, issuer_url, client_id, client_secret, default_role)
		VALUES (1, 'idp', 'IdP', 'https://idp.test', 'c', 's', 'viewer')`); err != nil {
		t.Fatal(err)
	}
	store := rbac.NewStore(database)
	s.SetAccessHooks(func(ctx context.Context) {
		if err := store.Load(ctx); err != nil {
			t.Error(err)
		}
	}, store.ReplaceIdPAssignments)
	if err := store.Load(ctx); err != nil {
		t.Fatal(err)
	}
	roleOf := func(u *User) string {
		var out []string
		for _, a := range store.UserAssignments(u.ID) {
			out = append(out, a.Role)
		}
		return strings.Join(out, ",")
	}
	local, err := s.CreateUser(ctx, "alice", "alice@example.com", "correct horse battery", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "root", "root@example.com", "correct horse battery", "admin"); err != nil {
		t.Fatal(err)
	}
	login := func(subject, email string, verified bool, role string) (*User, error) {
		return s.ProvisionSSOUser(ctx, SSOIdentity{Kind: "oidc", Provider: "idp", Subject: subject,
			Email: email, EmailVerified: verified, Username: "x",
			Assignments: []rbac.Assignment{{Role: role, Scope: rbac.Global, Source: rbac.SourceIdP, Via: "OIDC IdP group g"}}})
	}

	// An attacker's IdP account with someone's email doesn't take it over.
	if _, err := login("evil", "alice@example.com", false, "viewer"); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("unverified email: %v, want ErrSSOEmailInUse", err)
	}
	if _, err := login("evil", "ALICE@example.com", true, "viewer"); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("verified email, linking off: %v, want ErrSSOEmailInUse", err)
	}

	// With linking enabled and a verified email the identity links, and the
	// manually set access stays.
	if err := s.SetEmailLinking(ctx, "oidc", 1, true); err != nil {
		t.Fatal(err)
	}
	u, err := login("alice-sub", "alice@example.com", true, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != local.ID || roleOf(u) != "operator" {
		t.Fatalf("linked user = %s/%s, want %s/operator", u.ID, roleOf(u), local.ID)
	}
	if u, err = login("alice-sub", "", false, "viewer"); err != nil || roleOf(u) != "operator" {
		t.Fatalf("later login: %v roles %v", err, roleOf(u))
	}
	// A second identity can't take over the now-linked account, and admin
	// accounts are never linked by email.
	if _, err := login("other-sub", "alice@example.com", true, "viewer"); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("relink of a linked account: %v", err)
	}
	if _, err := login("root-sub", "root@example.com", true, "viewer"); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("link into admin: %v", err)
	}

	// New SSO accounts follow the IdP groups until an admin stops it.
	if _, err := login("bob-sub", "bob@example.com", false, "viewer"); err != nil {
		t.Fatal(err)
	}
	b, err := login("bob-sub", "bob@example.com", false, "operator")
	if err != nil || roleOf(b) != "operator" {
		t.Fatalf("sso role update: %v %v", err, roleOf(b))
	}
	if err := s.SetSource(ctx, b.ID, RoleSourceManual); err != nil {
		t.Fatal(err)
	}
	if b, err = login("bob-sub", "bob@example.com", false, "viewer"); err != nil || roleOf(b) != "operator" {
		t.Fatalf("access changed by sign-in after it was set to manual: %v %v", err, roleOf(b))
	}

	// Auto-create off: unknown identities are refused, known ones still sign in.
	settings[AutoCreateOnSSOKey] = "0"
	if _, err := login("carol-sub", "carol@example.com", true, "viewer"); !errors.Is(err, ErrSSOAutoCreateOff) {
		t.Fatalf("auto-create off: %v", err)
	}
	if _, err := login("bob-sub", "bob@example.com", false, "viewer"); err != nil {
		t.Fatalf("known identity with auto-create off: %v", err)
	}
}
