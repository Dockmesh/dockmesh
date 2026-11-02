package userprefs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
)

func TestValidLanguage(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"en", true},
		{"de", true},
		{"fr", false},
		{"DE", false},
		{"de-AT", false},
	}
	for _, c := range cases {
		if got := ValidLanguage(c.in); got != c.want {
			t.Errorf("ValidLanguage(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLanguageForEmail(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "prefs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users (id, username, email, password) VALUES
		('u1', 'anna', 'Anna@example.com', 'x'), ('u2', 'ben', 'ben@example.com', 'x'), ('u3', 'nomail', NULL, 'x')`); err != nil {
		t.Fatal(err)
	}
	s := New(database)
	ctx := context.Background()
	if err := s.Set(ctx, "u1", KeyLanguage, "de"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "u3", KeyLanguage, "de"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		email, want string
	}{
		{"anna@example.com", "de"},
		{"ben@example.com", ""},
		{"nobody@example.com", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, err := s.LanguageForEmail(ctx, c.email)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("LanguageForEmail(%q) = %q, want %q", c.email, got, c.want)
		}
	}
}
