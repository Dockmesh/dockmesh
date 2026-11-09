package selfupdate

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.0", "v0.1.1", true},
		{"v0.1.1", "v0.1.1", false},
		{"v0.1.2", "v0.1.1", false},
		{"0.1.0", "0.2.0", true},
		{"v1.0.0", "v0.9.9", false},
		{"v0.1.0", "v1.0.0", true},

		// Non-release current versions should never trigger an update.
		{"dev", "v0.1.1", false},
		{"", "v0.1.1", false},
		{"unknown", "v0.1.1", false},

		// Empty latest is "no check yet".
		{"v0.1.0", "", false},

		// Pre-release suffix on current should compare the base version.
		{"v0.1.0-rc1", "v0.1.0", false},
		{"v0.1.0-rc1", "v0.1.1", true},
	}
	for _, c := range cases {
		if got := isNewer(c.current, c.latest); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

type oneAdmin struct{}

func (oneAdmin) ListActiveUserIDs(context.Context) ([]string, error) { return []string{"admin"}, nil }
func (oneAdmin) CanReceive(context.Context, string, notifications.EmitInput) bool { return true }

func TestNotifyAvailableOncePerVersionAcrossRestarts(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := settings.NewStore(database)
	if _, err := database.Exec(`INSERT INTO users (id, username, password) VALUES ('admin', 'admin', 'x')`); err != nil {
		t.Fatal(err)
	}
	notifs := notifications.NewService(database)
	notifs.SetUserDirectory(oneAdmin{})
	notifs.SetAccessChecker(oneAdmin{})
	count := func() (n int, link string) {
		t.Helper()
		if err := database.QueryRow(`SELECT COUNT(*), COALESCE(MAX(link), '') FROM notifications`).Scan(&n, &link); err != nil {
			t.Fatal(err)
		}
		return n, link
	}

	for run := range 2 {
		c := New(store, "v0.5.0")
		c.SetNotifier(notifs)
		c.notifyAvailable(ctx, "v0.5.1")
		if n, link := count(); n != 1 || link != "/settings/updates" {
			t.Fatalf("after start %d: %d notifications, link %q", run+1, n, link)
		}
	}
	c := New(store, "v0.5.0")
	c.SetNotifier(notifs)
	c.notifyAvailable(ctx, "v0.5.2")
	if n, _ := count(); n != 2 {
		t.Fatalf("a newer release was not announced: %d notifications", n)
	}
}
