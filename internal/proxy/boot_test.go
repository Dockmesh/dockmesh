package proxy

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type memSettings map[string]string

func (m memSettings) Get(k, def string) string {
	if v, ok := m[k]; ok && v != "" {
		return v
	}
	return def
}

func (m memSettings) Set(_ context.Context, k, v string) error { m[k] = v; return nil }

func newBootDB(t *testing.T, routes int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE proxy_routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, upstream TEXT NOT NULL,
		tls_mode TEXT NOT NULL DEFAULT 'none', enabled INTEGER NOT NULL DEFAULT 1,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < routes; i++ {
		if _, err := db.Exec(`INSERT INTO proxy_routes (host, upstream) VALUES (?, '127.0.0.1:8080')`, "h"+string(rune('a'+i))+".example"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// fakeCaddy answers the admin API; loaded decides whether it reports routes.
func fakeCaddy(t *testing.T, loaded bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/config/apps/http/servers") && loaded {
			_, _ = w.Write([]byte(`{"dockmesh":{"routes":[{}]}}`))
			return
		}
		_, _ = w.Write([]byte(`null`))
	}))
	old := adminURL
	adminURL = srv.URL
	t.Cleanup(func() { adminURL = old; srv.Close() })
}

func TestWanted(t *testing.T) {
	tests := []struct {
		name        string
		setting     string
		envDefault  bool
		exists      bool
		dbRoutes    int
		caddyRoutes bool
		want        bool
		wantSaved   string
	}{
		{name: "saved on", setting: "true", exists: true, dbRoutes: 2, want: true, wantSaved: "true"},
		{name: "saved off stays off", setting: "false", exists: true, dbRoutes: 2, want: false, wantSaved: "false"},
		{name: "saved off beats env default", setting: "false", envDefault: true, want: false, wantSaved: "false"},
		{name: "unset follows env default", envDefault: true, want: true},
		{name: "unset with container and db routes is adopted", exists: true, dbRoutes: 1, want: true, wantSaved: "true"},
		{name: "unset with container and loaded caddy is adopted", exists: true, caddyRoutes: true, want: true, wantSaved: "true"},
		{name: "unset with empty container stays off", exists: true, want: false},
		{name: "unset without container stays off", dbRoutes: 3, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeCaddy(t, tt.caddyRoutes)
			st := memSettings{}
			if tt.setting != "" {
				st[KeyEnabled] = tt.setting
			}
			svc := NewService(newBootDB(t, tt.dbRoutes), nil, false)
			svc.containerState = func(context.Context) (bool, bool) { return tt.exists, tt.exists }
			if got := svc.wanted(context.Background(), st, tt.envDefault); got != tt.want {
				t.Fatalf("wanted = %v, want %v", got, tt.want)
			}
			if st[KeyEnabled] != tt.wantSaved {
				t.Fatalf("saved setting = %q, want %q", st[KeyEnabled], tt.wantSaved)
			}
		})
	}
}
