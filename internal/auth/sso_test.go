package auth

import (
	"context"
	"errors"
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
	local, err := s.CreateUser(ctx, "alice", "alice@example.com", "correct horse battery", "operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "root", "root@example.com", "correct horse battery", "admin"); err != nil {
		t.Fatal(err)
	}
	login := func(subject, email string, verified bool, role string) (*User, error) {
		return s.ProvisionSSOUser(ctx, SSOIdentity{Kind: "oidc", Provider: "idp", Subject: subject,
			Email: email, EmailVerified: verified, Username: "x", Role: role})
	}

	// An attacker's IdP account with someone's email doesn't take it over.
	if _, err := login("evil", "alice@example.com", false, "viewer"); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("unverified email: %v, want ErrSSOEmailInUse", err)
	}
	if _, err := login("evil", "ALICE@example.com", true, "viewer"); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("verified email, linking off: %v, want ErrSSOEmailInUse", err)
	}

	// With linking enabled and a verified email the identity links, and the
	// manually set role stays.
	if err := s.SetEmailLinking(ctx, "oidc", 1, true); err != nil {
		t.Fatal(err)
	}
	u, err := login("alice-sub", "alice@example.com", true, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != local.ID || u.Role != "operator" {
		t.Fatalf("linked user = %s/%s, want %s/operator", u.ID, u.Role, local.ID)
	}
	if u, err = login("alice-sub", "", false, "viewer"); err != nil || u.Role != "operator" {
		t.Fatalf("later login: %v role %v", err, u)
	}
	// A second identity can't take over the now-linked account, and admin
	// accounts are never linked by email.
	if _, err := login("other-sub", "alice@example.com", true, "viewer"); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("relink of a linked account: %v", err)
	}
	if _, err := login("root-sub", "root@example.com", true, "viewer"); !errors.Is(err, ErrSSOEmailInUse) {
		t.Fatalf("link into admin: %v", err)
	}

	// New SSO accounts follow the IdP role until an admin sets one.
	if _, err := login("bob-sub", "bob@example.com", false, "viewer"); err != nil {
		t.Fatal(err)
	}
	b, err := login("bob-sub", "bob@example.com", false, "operator")
	if err != nil || b.Role != "operator" {
		t.Fatalf("sso role update: %v %v", err, b)
	}
	if _, err := s.UpdateUser(ctx, b.ID, "bob@example.com", "deployer", ""); err != nil {
		t.Fatal(err)
	}
	if b, err = login("bob-sub", "bob@example.com", false, "viewer"); err != nil || b.Role != "deployer" {
		t.Fatalf("manual role overwritten: %v %v", err, b)
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
