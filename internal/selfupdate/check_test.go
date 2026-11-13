package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

		// A release candidate is older than its release.
		{"v0.1.0-rc1", "v0.1.0", true},
		{"v0.1.0-rc1", "v0.1.1", true},
		{"v0.1.0", "v0.1.0-rc.1", false},
		{"v0.6.0-rc.1", "v0.6.0-rc.2", true},
		{"v0.6.0-rc.2", "v0.6.0-rc.10", true},
		{"v0.6.0-rc.10", "v0.6.0-rc.2", false},
		{"v0.5.3", "v0.6.0-rc.1", true},
		{"v0.6.0-rc.1", "v0.5.3", false},
		{"v0.6.0-rc.1", "v0.6.0-rc.1", false},
		{"v0.6.0-alpha", "v0.6.0-alpha.1", true},
		{"v0.6.0-1", "v0.6.0-alpha", true},
		{"v0.6.0+abc", "v0.6.0", false},

		// Malformed tags never count as newer.
		{"v0.1.0", "v0.2", false},
		{"v0.1.0", "v0.2.0-", false},
		{"v0.1.0", "nightly", false},
		{"v0.1.0", "v0.2.0-rc.1;reboot", false},
	}
	for _, c := range cases {
		if got := isNewer(c.current, c.latest); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

type oneAdmin struct{}

func (oneAdmin) ListActiveUserIDs(context.Context) ([]string, error)              { return []string{"admin"}, nil }
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

func TestCheckNowChannels(t *testing.T) {
	stable := ghRelease{TagName: "v0.5.3"}
	rc1 := ghRelease{TagName: "v0.6.0-rc.1", Prerelease: true}
	rc2 := ghRelease{TagName: "v0.6.0-rc.2", Prerelease: true}
	final := ghRelease{TagName: "v0.6.0"}
	draft := ghRelease{TagName: "v0.7.0", Draft: true}
	tests := []struct {
		name        string
		channel     string
		current     string
		latest      ghRelease   // GET /releases/latest
		list        []ghRelease // GET /releases, newest first
		wantLatest  string
		wantPre     bool
		wantUpdate  bool
		wantChannel string
	}{
		{"unset channel is stable and ignores candidates", "", "v0.5.3", stable, []ghRelease{rc1, stable}, "v0.5.3", false, false, ChannelStable},
		{"stable ignores candidates", ChannelStable, "v0.5.2", stable, []ghRelease{rc1, stable}, "v0.5.3", false, true, ChannelStable},
		{"stable never takes a pre-release from latest", ChannelStable, "v0.5.3", rc1, nil, "", false, false, ChannelStable},
		{"stable on a candidate waits for the release", ChannelStable, "v0.6.0-rc.1", stable, nil, "v0.5.3", false, false, ChannelStable},
		{"stable on a candidate gets the release", ChannelStable, "v0.6.0-rc.2", final, nil, "v0.6.0", false, true, ChannelStable},
		{"pre-release offers a candidate", ChannelPrerelease, "v0.5.3", stable, []ghRelease{rc1, stable}, "v0.6.0-rc.1", true, true, ChannelPrerelease},
		{"pre-release offers the next candidate", ChannelPrerelease, "v0.6.0-rc.1", stable, []ghRelease{rc2, rc1, stable}, "v0.6.0-rc.2", true, true, ChannelPrerelease},
		{"pre-release prefers the release over its candidates", ChannelPrerelease, "v0.6.0-rc.2", stable, []ghRelease{final, rc2, rc1, stable}, "v0.6.0", false, true, ChannelPrerelease},
		{"pre-release offers a stable patch newer than every candidate", ChannelPrerelease, "v0.5.3", stable, []ghRelease{{TagName: "v0.6.1"}, final, rc2}, "v0.6.1", false, true, ChannelPrerelease},
		{"pre-release ignores drafts and junk tags", ChannelPrerelease, "v0.5.3", stable, []ghRelease{draft, {TagName: "nightly"}, stable}, "v0.5.3", false, false, ChannelPrerelease},
		{"pre-release order does not depend on the list order", ChannelPrerelease, "v0.5.3", stable, []ghRelease{stable, rc1, rc2}, "v0.6.0-rc.2", true, true, ChannelPrerelease},
	}
	st := newStore(t)
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/releases/latest":
					_ = json.NewEncoder(w).Encode(tt.latest)
				case "/releases":
					_ = json.NewEncoder(w).Encode(tt.list)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			for k, v := range map[string]string{ChannelKey: tt.channel, "update_latest_version": ""} {
				if err := st.Set(ctx, k, v); err != nil {
					t.Fatal(err)
				}
			}
			c := New(st, tt.current)
			c.apiBase = srv.URL
			if err := c.CheckNow(ctx); err != nil {
				t.Fatal(err)
			}
			got := c.Status()
			if got.LatestVersion != tt.wantLatest || got.LatestPrerelease != tt.wantPre || got.UpdateAvailable != tt.wantUpdate || got.Channel != tt.wantChannel {
				t.Errorf("status = latest %q pre %v update %v channel %q, want %q %v %v %q",
					got.LatestVersion, got.LatestPrerelease, got.UpdateAvailable, got.Channel, tt.wantLatest, tt.wantPre, tt.wantUpdate, tt.wantChannel)
			}
		})
	}
}

func TestSwitchingBackToStableHidesCachedCandidate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for k, v := range map[string]string{ChannelKey: ChannelPrerelease, "update_latest_version": "v0.6.0-rc.1"} {
		if err := st.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	c := New(st, "v0.5.3")
	if s := c.Status(); !s.UpdateAvailable || s.LatestVersion != "v0.6.0-rc.1" {
		t.Fatalf("pre-release channel status = %+v", s)
	}
	if err := st.Set(ctx, ChannelKey, ChannelStable); err != nil {
		t.Fatal(err)
	}
	if s := c.Status(); s.UpdateAvailable || s.LatestVersion != "" {
		t.Errorf("stable channel still offers the candidate: %+v", s)
	}
}

func newStore(t *testing.T) *settings.Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	return settings.NewStore(database)
}

func TestReleaseURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0", "https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0"},
		{"https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0-rc.1", "https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0-rc.1"},
		{"javascript:alert(1)", ""},
		{"http://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0", ""},
		{"https://github.com.evil.example/Dockmesh/dockmesh/releases/tag/v1", ""},
		{"https://user@github.com/Dockmesh/dockmesh/releases/tag/v1", ""},
		{"https://github.com/Dockmesh/dockmesh/releases/../../evil/repo", ""},
		{"https://github.com/Dockmesh/dockmesh/releases/%2e%2e/%2e%2e/evil", ""},
		{"https://github.com/evil/dockmesh/releases/tag/v1", ""},
		{"/settings", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ReleaseURL(tt.in); got != tt.want {
			t.Errorf("ReleaseURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStatusDropsForeignReleaseURL(t *testing.T) {
	st := newStore(t)
	if err := st.Set(context.Background(), "update_release_url", "javascript:alert(1)"); err != nil {
		t.Fatal(err)
	}
	if got := New(st, "v0.5.3").Status().ReleaseURL; got != "" {
		t.Errorf("release_url = %q, want empty", got)
	}
}
