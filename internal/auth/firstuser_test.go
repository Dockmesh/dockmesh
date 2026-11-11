package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestCreateFirstUser(t *testing.T) {
	tests := []struct {
		name     string
		existing string // role of an account created before, "-" for none
		wantErr  error
	}{
		{name: "empty install", existing: "-"},
		{name: "account without a role", existing: "", wantErr: ErrAlreadySetUp},
		{name: "viewer only", existing: "viewer", wantErr: ErrAlreadySetUp},
		{name: "admin", existing: "admin", wantErr: ErrAlreadySetUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, database := newTestService(t)
			if tt.existing != "-" {
				mustUser(ctx, t, s, "old", tt.existing)
			}
			_, err := s.CreateFirstUser(ctx, "wizard", "", "correct horse battery", "admin")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("CreateFirstUser: %v, want %v", err, tt.wantErr)
			}
			var users, admins int
			if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE username = 'wizard'`).Scan(&users); err != nil {
				t.Fatal(err)
			}
			if err := database.QueryRow(`SELECT COUNT(*) FROM role_assignments a JOIN users u ON u.id = a.user_id WHERE u.username = 'wizard'`).Scan(&admins); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tt.wantErr == nil {
				want = 1
			}
			if users != want || admins != want {
				t.Fatalf("wizard account rows %d, assignments %d, want %d", users, admins, want)
			}
		})
	}
}

func TestCreateFirstUserOnlyOnce(t *testing.T) {
	ctx := context.Background()
	s, database := newTestService(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.CreateFirstUser(ctx, "admin"+string(rune('a'+i)), "", "correct horse battery", "admin")
		}()
	}
	wg.Wait()
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d accounts created concurrently, want 1 (errors %v)", n, errs)
	}
}
