package proxy

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// fakeCaddy answers the admin API and counts /load pushes.
func fakeCaddy(t *testing.T, loaded bool) *atomic.Int32 {
	t.Helper()
	var loads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/load":
			loads.Add(1)
		case strings.HasPrefix(r.URL.Path, "/config/apps/http/servers"):
			if loaded {
				_, _ = w.Write([]byte(`{"dockmesh":{"routes":[{}]}}`))
			} else {
				_, _ = w.Write([]byte(`null`))
			}
		default:
			w.Header().Set("Server", "Caddy")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	old := adminURL
	adminURL = srv.URL
	t.Cleanup(func() { adminURL = old; srv.Close() })
	return &loads
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name        string
		setting     string
		envDefault  bool
		exists      bool
		dbRoutes    int
		caddyRoutes bool
		wantEnabled bool
		wantSaved   string
		wantLoads   int32
	}{
		{name: "saved on with running container pushes routes", setting: "true", exists: true, dbRoutes: 2, wantEnabled: true, wantSaved: "true", wantLoads: 1},
		{name: "saved off stays off", setting: "false", exists: true, dbRoutes: 2, wantEnabled: false, wantSaved: "false"},
		{name: "unset with container and db routes is adopted", exists: true, dbRoutes: 1, wantEnabled: true, wantSaved: "true", wantLoads: 1},
		{name: "unset with container and loaded caddy is adopted", exists: true, caddyRoutes: true, wantEnabled: true, wantSaved: "true", wantLoads: 1},
		{name: "unset with empty container stays off", exists: true, wantEnabled: false},
		{name: "unset without container stays off", dbRoutes: 3, wantEnabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loads := fakeCaddy(t, tt.caddyRoutes)
			st := memSettings{}
			if tt.setting != "" {
				st[KeyEnabled] = tt.setting
			}
			svc := NewService(newBootDB(t, tt.dbRoutes), nil, false)
			svc.containerState = func(context.Context) (bool, bool) { return tt.exists, tt.exists }
			got, err := svc.Reconcile(context.Background(), st, tt.envDefault)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantEnabled || svc.Enabled() != tt.wantEnabled {
				t.Fatalf("enabled = %v / service %v, want %v", got, svc.Enabled(), tt.wantEnabled)
			}
			if st[KeyEnabled] != tt.wantSaved {
				t.Fatalf("saved setting = %q, want %q", st[KeyEnabled], tt.wantSaved)
			}
			if loads.Load() != tt.wantLoads {
				t.Fatalf("config pushes = %d, want %d", loads.Load(), tt.wantLoads)
			}
		})
	}
}
