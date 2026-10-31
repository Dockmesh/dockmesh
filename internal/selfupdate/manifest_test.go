package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/settings"
)

func TestFetchManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok/release-manifest.json":
			_, _ = w.Write([]byte(`{"schema":1,"version":"0.5.0","highlights":["New overview."],"changes":{"added":["Feed."]}}`))
		case "/other/release-manifest.json":
			_, _ = w.Write([]byte(`{"schema":1,"version":"0.4.9"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	st := settings.NewStore(database)
	c := New(st, "v0.4.2")
	c.assetPrefix = srv.URL + "/"

	release := func(dir string) ghRelease {
		rel := ghRelease{TagName: "v0.5.0"}
		rel.Assets = []ghAsset{{Name: manifestAsset, URL: srv.URL + "/" + dir + "/" + manifestAsset}}
		return rel
	}
	tests := []struct {
		name string
		rel  ghRelease
		ok   bool
	}{
		{"matching manifest", release("ok"), true},
		{"manifest for another version", release("other"), false},
		{"missing asset", release("missing"), false},
		{"no assets", ghRelease{TagName: "v0.5.0"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.fetchManifest(context.Background(), tt.rel)
			if (got != "") != tt.ok {
				t.Errorf("fetchManifest = %q, want ok=%v", got, tt.ok)
			}
		})
	}

	c.assetPrefix = "https://github.com/Dockmesh/dockmesh/releases/download/"
	if got := c.fetchManifest(context.Background(), release("ok")); got != "" {
		t.Errorf("fetched a manifest outside the release asset prefix: %q", got)
	}

	ctx := context.Background()
	c.assetPrefix = srv.URL + "/"
	if err := st.Set(ctx, "update_latest_version", "v0.5.0"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, keyManifest, c.fetchManifest(ctx, release("ok"))); err != nil {
		t.Fatal(err)
	}
	m := c.LatestManifest()
	if m == nil || m.Version != "0.5.0" || len(m.Highlights) != 1 {
		t.Fatalf("LatestManifest = %+v", m)
	}
	if New(st, "v0.5.0").LatestManifest() != nil {
		t.Error("LatestManifest returned the running version's manifest")
	}
}
