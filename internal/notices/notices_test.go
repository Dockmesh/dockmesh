package notices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dockmesh/dockmesh/internal/db"
)

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
	for _, id := range []string{"u-admin", "u-op", "u-view"} {
		if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES (?, ?, 'x')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	return NewService(database), database
}

var (
	admin = Viewer{UserID: "u-admin", Role: "admin", Allowed: func(string) bool { return true }}
	op    = Viewer{UserID: "u-op", Role: "operator", Allowed: func(p string) bool { return p == "stacks.deploy" }}
	// Custom role that holds system.update without being called "admin".
	opsLead = Viewer{UserID: "u-op", Role: "ops-lead", Allowed: func(p string) bool { return p == "system.update" }}
	// A custom role reusing the name "admin" but stripped of system.update.
	fakeAdmin = Viewer{UserID: "u-op", Role: "admin", Allowed: func(string) bool { return false }}
	viewer    = Viewer{UserID: "u-view", Role: "viewer", Allowed: func(string) bool { return false }}
	token     = Viewer{Role: "admin", Allowed: func(string) bool { return true }}
)

func keys(ns []Notice) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Key)
	}
	return out
}

func TestAudience(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	for _, in := range []RaiseInput{
		{Key: "all", Audience: All(), Severity: SevInfo, Title: "everyone"},
		{Key: "admins", Audience: Admins(), Severity: SevWarning, Title: "admins only"},
		{Key: "users", Audience: Users("u-view"), Severity: SevInfo, Title: "just the viewer"},
		{Key: "perm", Audience: WithPermission("stacks.deploy"), Severity: SevInfo, Title: "deployers"},
	} {
		if err := s.Raise(ctx, in); err != nil {
			t.Fatalf("raise %s: %v", in.Key, err)
		}
	}
	tests := []struct {
		name string
		v    Viewer
		want map[string]bool
	}{
		{"admin", admin, map[string]bool{"all": true, "admins": true, "perm": true}},
		{"operator", op, map[string]bool{"all": true, "perm": true}},
		{"custom role with system.update", opsLead, map[string]bool{"all": true, "admins": true}},
		{"role named admin without system.update", fakeAdmin, map[string]bool{"all": true}},
		{"viewer", viewer, map[string]bool{"all": true, "users": true}},
		{"api token", token, map[string]bool{"all": true, "admins": true, "perm": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.ForViewer(ctx, tt.v)
			if err != nil {
				t.Fatal(err)
			}
			gotSet := map[string]bool{}
			for _, k := range keys(got) {
				gotSet[k] = true
			}
			if len(gotSet) != len(tt.want) {
				t.Fatalf("got %v, want %v", keys(got), tt.want)
			}
			for k := range tt.want {
				if !gotSet[k] {
					t.Errorf("missing %q in %v", k, keys(got))
				}
			}
		})
	}
}

func TestRaiseIsIdempotentAndReopens(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	in := RaiseInput{Key: "k", Audience: All(), Severity: SevWarning, Title: "first"}
	if err := s.Raise(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := s.Dismiss(ctx, viewer, mustOpen(t, s)[0].ID); err != nil {
		t.Fatal(err)
	}

	in.Title = "second"
	if err := s.Raise(ctx, in); err != nil {
		t.Fatal(err)
	}
	open := mustOpen(t, s)
	if len(open) != 1 || open[0].Title != "first" {
		t.Fatalf("raising an open key must be a no-op, got %+v", open)
	}

	if err := s.Resolve(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if got := mustOpen(t, s); len(got) != 0 {
		t.Fatalf("resolved notice still open: %+v", got)
	}
	if err := s.Resolve(ctx, "k"); err != nil {
		t.Fatalf("resolving twice: %v", err)
	}
	if err := s.Resolve(ctx, "never-raised"); err != nil {
		t.Fatalf("resolving unknown key: %v", err)
	}

	if err := s.Raise(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.ForViewer(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "second" || got[0].Dismissed {
		t.Fatalf("reopened notice should carry new content and no dismissal, got %+v", got)
	}
}

func TestRaiseWhileOtherWritersCommit(t *testing.T) {
	s, database := newTestService(t)
	ctx := context.Background()
	stop := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = database.Exec(`INSERT INTO notices (key, audience, severity, title) VALUES (?, 'all', 'info', 'noise')`,
				fmt.Sprintf("noise-%d", i))
			time.Sleep(time.Millisecond)
		}
	}()
	var raisers sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		raisers.Add(1)
		go func() {
			defer raisers.Done()
			errs <- s.Raise(ctx, RaiseInput{Key: fmt.Sprintf("k-%d", i), Audience: All(), Severity: SevInfo, Title: "t"})
		}()
	}
	raisers.Wait()
	close(stop)
	writers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("raise under concurrent writes: %v", err)
		}
	}
}

func TestDismiss(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	if err := s.Raise(ctx, RaiseInput{Key: "warn", Audience: All(), Severity: SevWarning, Title: "w"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Raise(ctx, RaiseInput{Key: "crit", Audience: All(), Severity: SevCritical, Title: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Raise(ctx, RaiseInput{Key: "adm", Audience: Admins(), Severity: SevInfo, Title: "a"}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, n := range mustOpen(t, s) {
		ids[n.Key] = n.ID
	}

	tests := []struct {
		name string
		v    Viewer
		key  string
		want error
	}{
		{"warning", viewer, "warn", nil},
		{"warning again", viewer, "warn", nil},
		{"critical", viewer, "crit", ErrNotDismissible},
		{"outside audience", viewer, "adm", ErrNotFound},
		{"api token has no user", token, "warn", ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.Dismiss(ctx, tt.v, ids[tt.key]); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}

	got, err := s.ForViewer(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range got {
		if n.Dismissed != (n.Key == "warn") {
			t.Errorf("%s dismissed=%v", n.Key, n.Dismissed)
		}
	}
	got, err = s.ForViewer(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range got {
		if n.Dismissed {
			t.Errorf("dismissal leaked to another user: %s", n.Key)
		}
	}
}

func TestRaiseValidation(t *testing.T) {
	s, _ := newTestService(t)
	ok := RaiseInput{Key: "k", Audience: All(), Severity: SevInfo, Title: "t", Link: "/stacks/x"}
	tests := []struct {
		name string
		mod  func(*RaiseInput)
	}{
		{"no key", func(in *RaiseInput) { in.Key = " " }},
		{"no title", func(in *RaiseInput) { in.Title = "" }},
		{"bad severity", func(in *RaiseInput) { in.Severity = "error" }},
		{"bad audience", func(in *RaiseInput) { in.Audience = Audience{Kind: "everyone"} }},
		{"users without ids", func(in *RaiseInput) { in.Audience = Users() }},
		{"permission without perm", func(in *RaiseInput) { in.Audience = WithPermission("") }},
		{"absolute link", func(in *RaiseInput) { in.Link = "https://evil.example/" }},
		{"protocol-relative link", func(in *RaiseInput) { in.Link = "//evil.example/" }},
		{"relative link", func(in *RaiseInput) { in.Link = "stacks/x" }},
	}
	if err := s.Raise(context.Background(), ok); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := ok
			in.Key = "k-" + tt.name
			tt.mod(&in)
			if err := s.Raise(context.Background(), in); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestRunChecks(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	condition := true
	ran := 0
	s.Register("broken", func(context.Context, *Service) error { ran++; return errors.New("boom") })
	s.Register("cond", func(ctx context.Context, s *Service) error {
		ran++
		return s.Set(ctx, condition, RaiseInput{Key: "cond", Audience: Admins(), Severity: SevWarning, Title: "c"})
	})

	s.RunChecks(ctx)
	if ran != 2 {
		t.Fatalf("a failing check must not stop the others, ran=%d", ran)
	}
	if got := keys(mustOpen(t, s)); len(got) != 1 || got[0] != "cond" {
		t.Fatalf("open = %v", got)
	}
	condition = false
	s.RunChecks(ctx)
	if got := mustOpen(t, s); len(got) != 0 {
		t.Fatalf("condition gone but notice open: %v", keys(got))
	}
}

func TestDeletedUserDismissalsGo(t *testing.T) {
	s, database := newTestService(t)
	ctx := context.Background()
	if err := s.Raise(ctx, RaiseInput{Key: "k", Audience: All(), Severity: SevInfo, Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Dismiss(ctx, viewer, mustOpen(t, s)[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM users WHERE id = 'u-view'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM notice_dismissals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("dismissals of a deleted user remain: %d", n)
	}
}

func mustOpen(t *testing.T, s *Service) []Notice {
	t.Helper()
	open, err := s.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return open
}

func TestBullets(t *testing.T) {
	tests := []struct {
		name  string
		items []string
		want  string
	}{
		{name: "two items", items: []string{"web", "db"}, want: "Stacks:\n- web\n- db\nFix them."},
		{name: "one item", items: []string{"web"}, want: "Stacks:\n- web\nFix them."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := "Stacks:" + Bullets(tt.items) + "Fix them."; got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
