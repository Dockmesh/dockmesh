package auth

import (
	"context"
	"errors"
	"testing"
)

func (s *Service) limitRow(t *testing.T, userID string) (failed, count int, since int64) {
	t.Helper()
	if err := s.db.QueryRow(
		`SELECT failed_login_attempts, login_limit_count, login_limit_since FROM users WHERE id = ?`, userID).
		Scan(&failed, &count, &since); err != nil {
		t.Fatal(err)
	}
	return failed, count, since
}

func TestLoginRecordsFailureAfterHangup(t *testing.T) {
	s, _ := newTestService(t)
	u := mustUser(context.Background(), t, s, "hank", "viewer")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.afterAcquire = cancel
	if _, err := s.Login(ctx, "hank", "wrong", "test", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	if failed, count, _ := s.limitRow(t, u.ID); failed != 1 || count != 1 {
		t.Errorf("after a hung-up failure: failed_login_attempts = %d, login_limit_count = %d, want 1 and 1", failed, count)
	}
}

func TestUnknownNamesWhenLimiterFull(t *testing.T) {
	s, _ := newTestService(t)
	s.unknownLimit.MaxKeys = 1
	tests := []string{"first", "second", "third"}
	for _, name := range tests {
		if _, err := s.Login(context.Background(), name, "wrong", "test", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) ||
			errors.Is(err, ErrTooManyAttempts) {
			t.Fatalf("%s: err = %v, want the normal invalid-credentials answer", name, err)
		}
	}
}

func TestRefundAccountSkipsReplacedWindow(t *testing.T) {
	tests := []struct {
		name       string
		resetFirst bool
		wantCount  int
	}{
		{name: "same window", wantCount: 0},
		{name: "window replaced", resetFirst: true, wantCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestService(t)
			ctx := context.Background()
			u := mustUser(ctx, t, s, "ivy", "viewer")
			a, ok, _, err := s.acquireAccount(ctx, u.ID, "ivy")
			if err != nil || !ok {
				t.Fatalf("acquire: %v %v", ok, err)
			}
			if tt.resetFirst {
				// Another request opened a new window after this one counted.
				if _, err := s.db.Exec(`UPDATE users SET login_limit_count = 1, login_limit_since = login_limit_since + 1000 WHERE id = ?`, u.ID); err != nil {
					t.Fatal(err)
				}
			}
			s.refundAccount(ctx, a)
			if _, count, _ := s.limitRow(t, u.ID); count != tt.wantCount {
				t.Errorf("login_limit_count = %d, want %d", count, tt.wantCount)
			}
		})
	}
}
