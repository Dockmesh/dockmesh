package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// useTestAdmin points the admin client at srv for the test.
func useTestAdmin(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := adminEP.Load()
	adminEP.Store(&adminEndpoint{url: srv.URL})
	t.Cleanup(func() { adminEP.Store(old); srv.Close() })
}

// adminServer fakes an admin API that answers to id and counts /load calls.
func adminServer(t *testing.T, id string) *atomic.Int32 {
	t.Helper()
	var loads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/id/"+id && id != "":
			_, _ = w.Write([]byte(`{"listen":"127.0.0.1:2019"}`))
		case strings.HasPrefix(r.URL.Path, "/id/"):
			http.Error(w, `{"error":"unknown object ID"}`, http.StatusNotFound)
		case r.URL.Path == "/load":
			loads.Add(1)
		default:
			_, _ = w.Write([]byte(`null`))
		}
	}))
	useTestAdmin(t, srv)
	return &loads
}

// Anything else listening on the admin port never receives the config.
func TestPushConfigOnlyToManagedCaddy(t *testing.T) {
	id, err := newAdminID()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		serverID  string
		configID  string
		wantLoads int32
		wantErr   error
	}{
		{"managed caddy", id, id, 1, nil},
		{"other process", "", id, 0, errNotManagedCaddy},
		{"other caddy", "dockmesh-admin-ffffffffffffffffffffffffffffffff", id, 0, errNotManagedCaddy},
		{"no id in config", id, "", 0, errNotManagedCaddy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loads := adminServer(t, tc.serverID)
			s := newServiceDB(t)
			s.enabled.Store(true)
			if tc.configID != "" {
				id := tc.configID
				s.adminID.Store(&id)
			}
			err := s.SyncFromDB(context.Background())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("pushConfig: %v, want %v", err, tc.wantErr)
			}
			if got := loads.Load(); got != tc.wantLoads {
				t.Fatalf("/load called %d times, want %d", got, tc.wantLoads)
			}
		})
	}
}

func TestBootstrapConfigCarriesAdminID(t *testing.T) {
	id, err := newAdminID()
	if err != nil {
		t.Fatal(err)
	}
	if !validAdminID(id) {
		t.Fatalf("generated id %q not valid", id)
	}
	b, err := bootstrapConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"admin":{"@id":"` + id + `","listen":"` + adminListen + `","origins":["` + adminHost + `"],"enforce_origin":true},"apps":{"http":{"servers":null}}}`; string(b) != want {
		t.Fatalf("bootstrap = %s\nwant        %s", b, want)
	}
}

// Per-site traffic figures need Caddy's HTTP metrics with the host label.
func TestBuildConfigEnablesPerHostMetrics(t *testing.T) {
	cfg, _ := buildConfig(nil, Settings{})
	if cfg.Apps.HTTP.Metrics == nil || !cfg.Apps.HTTP.Metrics.PerHost {
		t.Fatalf("http metrics = %+v, want per_host", cfg.Apps.HTTP.Metrics)
	}
}
