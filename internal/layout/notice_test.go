package layout

import (
	"context"
	"errors"
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

func TestCheckoutProbe(t *testing.T) {
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
	put("/data/dockmesh.db")
	put("/var/lib/dockmesh/data/dockmesh.db")
	put("/opt/other/data/secrets.env")
	trusted := func(string) error { return nil }
	distrusted := func(dir string) error { return errors.New(dir + " is writable by others") }
	tests := []struct {
		name, dir string
		extra     []string
		trust     func(string) error
		want      string
		wantErr   bool
	}{
		{name: "source run", dir: "/opt/src", trust: trusted, want: "/opt/src"},
		{name: "trailing slash", dir: "/opt/src/", trust: trusted, want: "/opt/src"},
		{name: "relative", dir: "opt/src", trust: trusted},
		{name: "no database", dir: "/opt/other", trust: trusted},
		{name: "default data dir", dir: config.DefaultDataDir, trust: trusted},
		{name: "systemd working directory", dir: "/", trust: trusted},
		{name: "missing extra env file", dir: "/opt/src", extra: []string{"/srv/unit/dockmesh.env"}, trust: trusted, want: "/opt/src"},
		{name: "untrusted", dir: "/opt/src", trust: distrusted, wantErr: true},
		{name: "untrusted without database", dir: "/opt/other", trust: distrusted},
	}
	for _, tt := range tests {
		p := CheckoutProbe{WorkDir: tt.dir, Getenv: func(string) string { return "" }, EnvFiles: tt.extra, Lstat: lstat, Trust: tt.trust}
		got, err := p.Dir()
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%s: got %q, %v; want %q, error %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestCheckoutNotice(t *testing.T) {
	ctx := context.Background()
	const migrate = "sudo /opt/dockmesh-src/dockmesh system migrate-layout --checkout /opt/dockmesh-src"
	for _, tt := range []struct {
		name, dir, binary string
		rootTrusted       bool
		want              bool
		wantBody, notBody string
	}{
		{name: "root-owned checkout", dir: "/opt/dockmesh-src", binary: "/opt/dockmesh-src/dockmesh", rootTrusted: true, want: true, wantBody: migrate},
		{name: "user-owned checkout", dir: "/opt/dockmesh-src", binary: "/opt/dockmesh-src/dockmesh", want: true, wantBody: "DOCKMESH_DATA_DIR=/opt/dockmesh-src", notBody: "migrate-layout"},
		{
			name: "paths with spaces", dir: "/srv/my src", binary: "/srv/my src/dockmesh", rootTrusted: true, want: true,
			wantBody: "sudo '/srv/my src/dockmesh' system migrate-layout --checkout '/srv/my src'",
		},
		{name: "user-owned checkout with spaces", dir: "/srv/my src", binary: "/srv/my src/dockmesh", want: true, wantBody: "DOCKMESH_DATA_DIR='/srv/my src'", notBody: "migrate-layout"},
		{name: "no checkout", binary: "/opt/dockmesh-src/dockmesh"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := dbtest.Open(filepath.Join(t.TempDir(), "dockmesh.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ns := notices.NewService(conn)
			// Every start runs the check; the second must not add a notice.
			for range 2 {
				if err := CheckoutNoticeCheck(tt.dir, tt.binary, tt.rootTrusted)(ctx, ns); err != nil {
					t.Fatal(err)
				}
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
				t.Fatalf("%d notices open, want open = %v", len(found), tt.want)
			}
			if !tt.want || runtime.GOOS != "linux" {
				return
			}
			if body := found[0].Body; !strings.Contains(body, tt.wantBody) || tt.notBody != "" && strings.Contains(body, tt.notBody) {
				t.Errorf("body = %q, want %q in it and not %q", body, tt.wantBody, tt.notBody)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"/opt/dockmesh-src", "/opt/dockmesh-src"},
		{"/srv/my src", "'/srv/my src'"},
		{"/srv/it's", `'/srv/it'\''s'`},
		{"/srv/$HOME;x", "'/srv/$HOME;x'"},
		{"", "''"},
	} {
		if got := ShellQuote(tt.in); got != tt.want {
			t.Errorf("ShellQuote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestHasCheckoutDB(t *testing.T) {
	root := t.TempDir()
	lstat := func(p string) (fs.FileInfo, error) { return os.Lstat(filepath.Join(root, p)) }
	for _, d := range []string{"/reg/data", "/link/data", "/dir/data/dockmesh.db"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "/reg/data/dockmesh.db"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "/reg/data/dockmesh.db"), filepath.Join(root, "/link/data/dockmesh.db")); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		dir  string
		want bool
	}{{"/reg", true}, {"/link", true}, {"/dir", false}, {"/missing", false}, {"reg", false}} {
		if got := HasCheckoutDB(tt.dir, lstat); got != tt.want {
			t.Errorf("HasCheckoutDB(%q) = %v, want %v", tt.dir, got, tt.want)
		}
	}
}
