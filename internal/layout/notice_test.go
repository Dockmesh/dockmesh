package layout

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db/dbtest"
	"github.com/dockmesh/dockmesh/internal/notices"
)

func TestLegacyEnvFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the config only moves on Linux")
	}
	dir := t.TempDir()
	regular := filepath.Join(dir, "dockmesh.env")
	link := filepath.Join(dir, "link.env")
	config := filepath.Join(dir, "etc.env")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(config, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, env, want string }{
		{"unknown env file", "", ""},
		{"runs from the config", config, ""},
		{"old place, moved", link, ""},
		{"old place, not moved", regular, regular},
		{"missing file", filepath.Join(dir, "gone.env"), ""},
	}
	for _, tt := range tests {
		if got := LegacyEnvFile(tt.env, config); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCheckoutDir(t *testing.T) {
	root := t.TempDir()
	lstat := func(p string) (fs.FileInfo, error) { return os.Lstat(filepath.Join(root, p)) }
	put := func(p string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("/opt/src/data/dockmesh.db")
	put("/var/lib/dockmesh/data/dockmesh.db")
	put("/opt/other/data/secrets.env")
	none := func(string) string { return "" }
	tests := []struct {
		name, dir string
		extra     []string
		want      string
	}{
		{"source run", "/opt/src", nil, "/opt/src"},
		{"trailing slash", "/opt/src/", nil, "/opt/src"},
		{"relative", "opt/src", nil, ""},
		{"no database", "/opt/other", nil, ""},
		{"default data dir", config.DefaultDataDir, nil, ""},
		{"missing extra env file", "/opt/src", []string{"/srv/unit/dockmesh.env"}, "/opt/src"},
	}
	for _, tt := range tests {
		if got := CheckoutDir(tt.dir, none, tt.extra, lstat); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCheckoutNotice(t *testing.T) {
	ctx := context.Background()
	conn, err := dbtest.Open(filepath.Join(t.TempDir(), "dockmesh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ns := notices.NewService(conn)
	for _, tt := range []struct {
		dir  string
		want bool
	}{{"/opt/dockmesh-src", true}, {"/opt/dockmesh-src", true}, {"", false}} {
		if err := CheckoutNoticeCheck(tt.dir)(ctx, ns); err != nil {
			t.Fatal(err)
		}
		open, err := ns.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var found []notices.Notice
		for _, n := range open {
			if n.Key == CheckoutNoticeKey {
				found = append(found, n)
			}
		}
		if len(found) > 1 || (len(found) == 1) != tt.want {
			t.Fatalf("dir %q: %d notices open, want open = %v", tt.dir, len(found), tt.want)
		}
		if tt.want && !strings.Contains(found[0].Body, tt.dir) {
			t.Errorf("body does not name the dir: %q", found[0].Body)
		}
		if tt.want && runtime.GOOS == "linux" && !strings.Contains(found[0].Body, "system migrate-layout") {
			t.Errorf("body does not name migrate-layout: %q", found[0].Body)
		}
	}
}
