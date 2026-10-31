package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The JSON fixtures are `caddy adapt` output of the Caddyfiles next to
// them, produced by the pinned Caddy image.
func loadAdapted(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "import", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func sitesByHost(res *ImportResult) map[string]ImportSite {
	out := map[string]ImportSite{}
	for _, s := range res.Sites {
		out[s.Host] = s
	}
	return out
}

func TestMapAdaptedSupported(t *testing.T) {
	res := mapAdapted(loadAdapted(t, "supported"))
	sites := sitesByHost(res)
	if len(sites) != 4 || len(res.Global) != 0 {
		t.Fatalf("sites = %v, global = %v", res.Sites, res.Global)
	}
	for h, s := range sites {
		if s.Status != ImportReady || len(s.Unsupported) > 0 {
			t.Errorf("%s: status %s, unsupported %v, error %q", h, s.Status, s.Unsupported, s.Error)
		}
	}

	photos := sites["photos.example.org"]
	p, r, hd := photos.Options.Performance, photos.Options.Routing, photos.Options.Headers
	if photos.Upstream != "192.168.10.40:2283" || photos.TLSMode != "auto" ||
		len(r.ExtraUpstreams) != 1 || r.LBPolicy != "round_robin" || r.HealthPath != "/api/server/ping" {
		t.Errorf("photos routing = %+v %+v", photos, r)
	}
	if !p.FlushImmediately || !p.Compression || p.MaxBodyBytes != 10_000_000_000 || p.StreamCloseDelay != "5m0s" ||
		p.DialTimeout != "30s" || p.ReadTimeout != "1h0m0s" || p.WriteTimeout != "1h0m0s" {
		t.Errorf("photos performance = %+v", p)
	}
	if !hd.Security || !photos.Options.HTTPS.HSTS || len(hd.ResponseSet) != 0 ||
		len(hd.ResponseRemove) != 1 || len(hd.RequestSet) != 1 || hd.RequestSet[0].Name != "X-Forwarded-Proto" {
		t.Errorf("photos headers = %+v", hd)
	}
	if !photos.Options.Logs.AccessLog {
		t.Error("photos access log not imported")
	}

	docs := sites["docs.example.org"]
	a := docs.Options.Access
	if !docs.Options.Routing.WWWRedirect {
		t.Error("www redirect not folded into docs.example.org")
	}
	if a.IPMode != "allow" || len(a.IPRanges) != 2 || len(a.BasicAuth) != 1 || a.BasicAuth[0].Hash != testHash {
		t.Errorf("docs access = %+v", a)
	}
	if a.ForwardAuth == nil || a.ForwardAuth.URL != "http://authelia:9091/api/verify?rd=https://auth.example.org" ||
		len(a.ForwardAuth.CopyHeaders) != 2 {
		t.Errorf("docs forward auth = %+v", a.ForwardAuth)
	}
	if sites["nas.home.lab"].TLSMode != "internal" || sites["plain.home.lab"].TLSMode != "none" {
		t.Errorf("tls modes: nas %s, plain %s", sites["nas.home.lab"].TLSMode, sites["plain.home.lab"].TLSMode)
	}
	if _, ok := sites["www.docs.example.org"]; ok {
		t.Error("www redirect imported as a site")
	}
}

func TestMapAdaptedPartial(t *testing.T) {
	res := mapAdapted(loadAdapted(t, "partial"))
	sites := sitesByHost(res)
	if len(sites) != 4 {
		t.Fatalf("sites = %v", res.Sites)
	}
	if s := sites["files.example.org"]; s.Status != ImportSkipped || !strings.Contains(s.Error, "no reverse_proxy") ||
		!containsSub(s.Unsupported, "file_server") {
		t.Errorf("files = %+v", s)
	}
	wiki := sites["wiki.example.org"]
	if wiki.Status != ImportPartial || wiki.Upstream != "wiki:3000" || !wiki.Options.Performance.Compression {
		t.Errorf("wiki = %+v", wiki)
	}
	for _, want := range []string{"path /api/*", "X-Real-Ip", "transport tls", "several names", "other encodings"} {
		if !containsSub(wiki.Unsupported, want) {
			t.Errorf("wiki unsupported %v lacks %q", wiki.Unsupported, want)
		}
	}
	if len(wiki.Options.Headers.RequestSet) != 0 {
		t.Errorf("placeholder header imported: %+v", wiki.Options.Headers.RequestSet)
	}
	if sites["wiki2.example.org"].Status != ImportPartial {
		t.Errorf("wiki2 = %+v", sites["wiki2.example.org"])
	}
	app := sites["app.example.org"]
	if app.Status != ImportSkipped || !strings.Contains(app.Error, "admin API") || !containsSub(app.Unsupported, "own certificate") {
		t.Errorf("app = %+v", app)
	}
	if !containsSub(res.Global, "certificate email") || !containsSub(res.Global, "own certificates") {
		t.Errorf("global = %v", res.Global)
	}
}

func containsSub(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestScreenCaddyfile(t *testing.T) {
	tests := []struct {
		name string
		text string
		ok   bool
	}{
		{"plain", "a.example.com {\n\treverse_proxy b:80\n}\n", true},
		{"snippet import", "(common) {\n\tencode gzip\n}\na.example.com {\n\timport common\n\treverse_proxy b:80\n}\n", false},
		{"snippet defined after a file import", "a.example.com {\n\timport /etc/passwd\n}\n(/etc/passwd) {\n}\n", false},
		{"client ca file", "a.example.com {\n\ttls {\n\t\tclient_auth {\n\t\t\ttrusted_ca_cert_file /etc/passwd\n\t\t}\n\t}\n}\n", false},
		{"file import", "a.example.com {\n\timport /data/caddy/certificates/x.key\n}\n", false},
		{"glob import", "import /etc/*\n", false},
		{"import on the block line", "a.example.com { import /config/caddy/autosave.json }\n", false},
		{"quoted import", "a.example.com {\n\t\"import\" /etc/passwd\n}\n", false},
		{"env substitution", "a.example.com {\n\treverse_proxy {$UPSTREAM}\n}\n", false},
		{"too large", strings.Repeat("#", MaxCaddyfileSize+1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := screenCaddyfile(tt.text)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrCaddyfileRejected) {
				t.Fatalf("err = %v is not ErrCaddyfileRejected", err)
			}
		})
	}
}

// fakeAdapt answers /adapt with the fixture of the posted Caddyfile, or
// with Caddy's error for the invalid one.
func fakeAdapt(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/adapt" {
			_, _ = w.Write([]byte(`null`))
			return
		}
		if r.Header.Get("Content-Type") != "text/caddyfile" || r.Header.Get("Origin") == "" {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		for _, name := range []string{"supported", "partial"} {
			want, _ := os.ReadFile(filepath.Join("testdata", "import", name+".Caddyfile"))
			if string(body) == strings.ReplaceAll(string(want), "\r\n", "\n") {
				result, _ := os.ReadFile(filepath.Join("testdata", "import", name+".json"))
				_, _ = w.Write([]byte(`{"warnings":[{"file":"Caddyfile","line":3,"directive":"encode","message":"example warning"}],"result":` + string(result) + `}`))
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"adapting config using caddyfile: unexpected EOF, at Caddyfile:2"}`))
	}))
	old := adminURL
	adminURL = srv.URL
	t.Cleanup(func() { adminURL = old; srv.Close() })
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "import", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func TestImportCaddyfile(t *testing.T) {
	ctx := context.Background()
	fakeAdapt(t)
	s := newServiceDB(t)
	if _, err := s.ImportCaddyfile(ctx, "a {\n}\n", false); !errors.Is(err, ErrProxyNotConfigured) {
		t.Fatalf("import with proxy off: err = %v", err)
	}
	s.enabled.Store(true)
	if _, err := s.CreateRoute(ctx, RouteSpec{Host: "nas.home.lab", Upstream: "10.0.0.1:80", TLSMode: "auto"}); err != nil && !strings.Contains(err.Error(), "caddy") {
		t.Fatal(err)
	}

	if _, err := s.ImportCaddyfile(ctx, readFixture(t, "invalid.Caddyfile"), false); !errors.Is(err, ErrCaddyfileInvalid) || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("invalid caddyfile: err = %v", err)
	}

	text := readFixture(t, "supported.Caddyfile")
	dry, err := s.ImportCaddyfile(ctx, text, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRoutes(t, s); n != 1 {
		t.Fatalf("dry run created routes: %d", n)
	}
	if len(dry.Warnings) != 1 || !strings.Contains(dry.Warnings[0], "line 3") {
		t.Fatalf("warnings = %v", dry.Warnings)
	}

	res, err := s.ImportCaddyfile(ctx, text, false)
	if err != nil {
		t.Fatal(err)
	}
	sites := sitesByHost(res)
	if nas := sites["nas.home.lab"]; nas.Status != ImportSkipped || nas.RouteID != 0 {
		t.Fatalf("existing host imported: %+v", nas)
	}
	docs := sites["docs.example.org"]
	if docs.RouteID == 0 || docs.Options.Access.BasicAuth[0].Hash != "" {
		t.Fatalf("docs = %+v", docs)
	}
	stored, err := s.getRoute(ctx, docs.RouteID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled || stored.Options.Access.BasicAuth[0].Hash != testHash || !stored.Options.Routing.WWWRedirect {
		t.Fatalf("stored docs = %+v", stored)
	}
	if n := countRoutes(t, s); n != 4 {
		t.Fatalf("routes after import = %d, want 4", n)
	}
}

func countRoutes(t *testing.T, s *Service) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM proxy_routes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
