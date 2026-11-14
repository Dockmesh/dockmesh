package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
)

func TestUnlockUserClearsLoginLimit(t *testing.T) {
	tests := []struct {
		name string
		arg  func(u *auth.User) string
	}{
		{name: "by username", arg: func(u *auth.User) string { return u.Username }},
		{name: "by id", arg: func(u *auth.User) string { return u.ID }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			svc := auth.NewService(database, []byte("0123456789abcdef0123456789abcdef"))
			const pw = "correct horse battery"
			u, err := svc.CreateUser(ctx, "frank", "", pw, "viewer")
			if err != nil {
				t.Fatal(err)
			}
			// A different case of the name counts toward frank's limit
			// without tripping the policy lockout first.
			for range auth.AccountLimitMax {
				if _, err := svc.Login(ctx, "FRANK", "wrong", "test", "127.0.0.1"); !errors.Is(err, auth.ErrInvalidCredentials) {
					t.Fatalf("wrong password: %v", err)
				}
			}
			if _, err := svc.Login(ctx, "frank", pw, "test", "127.0.0.1"); !errors.Is(err, auth.ErrTooManyAttempts) {
				t.Fatalf("blocked account: err = %v, want ErrTooManyAttempts", err)
			}
			if _, err := unlockUser(ctx, database, tt.arg(u)); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Login(ctx, "frank", pw, "test", "127.0.0.1"); err != nil {
				t.Fatalf("after unlock: %v", err)
			}
		})
	}
}

func TestCLIAuthServiceUsesStoredPolicy(t *testing.T) {
	ctx := context.Background()
	database, err := dbtest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`INSERT INTO settings (key, value) VALUES (?, '16'), (?, 'true')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, auth.PolicyMinLengthKey, auth.PolicyRequireDigitKey); err != nil {
		t.Fatal(err)
	}
	svc, err := cliAuthService(database)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, password string
		ok             bool
	}{
		{"shorter than the stored minimum", "fourteen chars", false},
		{"long enough without a digit", "sixteen characters", false},
		{"meets the stored policy", "sixteen characters 1", true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreateUser(ctx, fmt.Sprintf("new%d", i), "", tt.password, "viewer")
			if (err == nil) != tt.ok {
				t.Fatalf("admin create: err = %v, want ok = %v", err, tt.ok)
			}
		})
	}
	u, err := svc.CreateUser(ctx, "reset", "", "sixteen characters 2", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangePassword(ctx, u.ID, "fourteen chars"); err == nil {
		t.Fatal("reset-password accepted a password below the stored minimum")
	}
}
