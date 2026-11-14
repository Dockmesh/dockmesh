package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func prefix(path string) PathMatch { return PathMatch{Type: MatchPrefix, Path: path} }
func exact(path string) PathMatch  { return PathMatch{Type: MatchExact, Path: path} }

func toSite(m PathMatch) PathRoute {
	return PathRoute{Match: m, Action: ActionProxy, Target: PathTarget{Kind: TargetSite}}
}

func boolPtr(b bool) *bool { return &b }

// apexSite has /ords and /i to the site's HTTPS backend, /i with its
// own caching, everything else redirected.
func apexSite() Route {
	i := toSite(prefix("/i"))
	i.Headers.ResponseSet = []Header{{Name: "Cache-Control", Value: "public, max-age=86400"}}
	i.Headers.ResponseRemove = []string{"X-Frame-Options"}
	ords := toSite(prefix("/ords"))
	ords.Transport = PathTransport{ReadTimeout: "10m", FlushImmediately: boolPtr(true)}
	ords.Headers.RequestSet = []Header{{Name: "X-Env", Value: "ords"}}
	return Route{Host: "apex.example.com", Upstream: "10.0.0.20:8443", TLSMode: "auto", Enabled: true,
		Options: SiteOptions{
			Routing: RoutingOptions{
				UpstreamTLS: UpstreamTLS{TLS: true, ServerName: "apex.internal"},
				Fallback:    Fallback{Mode: FallbackRedirect, To: "/ords/r/app/home", Code: 302},
			},
			Headers: HeaderOptions{Security: true, ForwardedPort: true, RequestSet: []Header{{Name: "X-Env", Value: "site"}},
				ResponseSet: []Header{{Name: "Cache-Control", Value: "no-store"}}},
			HTTPS: HTTPSOptions{AllowHTTP: true},
		},
		Paths: []PathRoute{i, ords},
	}
}

func TestBuildConfigGoldenPaths(t *testing.T) {
	static := PathRoute{Match: prefix("/api"), Action: ActionProxy, StripPrefix: true,
		Target: PathTarget{Kind: TargetStatic, Upstreams: []string{"10.0.0.30:443", "10.0.0.31:443"}, LBPolicy: "first", HealthPath: "/healthz",
			TLS: UpstreamTLS{TLS: true, SkipVerify: true, HostHeader: "upstream"}},
		Headers:   PathHeaders{RequestSet: []Header{{Name: "Authorization", Value: "Bearer x", Secret: true}}, RequestRemove: []string{"Cookie"}},
		Transport: PathTransport{DialTimeout: "3s", BufferRequests: boolPtr(true)},
	}
	tests := []struct {
		name  string
		route Route
	}{
		{"paths_apex", apexSite()},
		{"paths_static", site("auto", SiteOptions{})},
		{"paths_actions", site("none", SiteOptions{Routing: RoutingOptions{Fallback: Fallback{Mode: FallbackNotFound}}})},
	}
	tests[1].route.Paths = []PathRoute{static}
	tests[2].route.Paths = []PathRoute{
		{Match: exact("/old"), Action: ActionRedirect, To: "https://new.example.com/", Code: 308},
		{Match: prefix("/admin"), Action: ActionRespond, Status: 403},
		toSite(exact("/health")),
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizePaths(tt.route.Paths)
			if err := tt.route.Options.validate(tt.route.Host, tt.route.TLSMode); err != nil {
				t.Fatalf("fixture options invalid: %v", err)
			}
			if err := validatePaths(tt.route.Paths); err != nil {
				t.Fatalf("fixture paths invalid: %v", err)
			}
			cfg, blocked := buildConfig([]Route{tt.route}, Settings{})
			if len(blocked) > 0 {
				t.Fatalf("blocked = %v", blocked)
			}
			checkGolden(t, filepath.Join("testdata", "config", tt.name+".json"), cfg)
		})
	}
}

// Removing every path gives back the config the site had without them.
func TestPathsRemovedGiveSameConfig(t *testing.T) {
	r := apexSite()
	without := r
	without.Paths = nil
	want, _ := buildConfig([]Route{without}, Settings{})
	r.Paths = []PathRoute{}
	got, _ := buildConfig([]Route{r}, Settings{})
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Fatalf("config with an empty path list differs:\n%s\n%s", b, a)
	}
}

func TestSortPaths(t *testing.T) {
	p := []PathRoute{toSite(prefix("/a")), toSite(prefix("/ords/r")), toSite(exact("/zz")), toSite(prefix("/ords")), toSite(exact("/a"))}
	sortPaths(p)
	var got []string
	for _, r := range p {
		got = append(got, r.Match.Type+" "+r.Match.Path)
	}
	want := "exact /zz,exact /a,prefix /ords/r,prefix /ords,prefix /a"
	if strings.Join(got, ",") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}

func TestValidatePaths(t *testing.T) {
	static := func(ups ...string) PathTarget { return PathTarget{Kind: TargetStatic, Upstreams: ups} }
	tests := []struct {
		name  string
		paths []PathRoute
		field string
	}{
		{"site target", []PathRoute{toSite(prefix("/ords"))}, ""},
		{"static target", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: static("10.0.0.1:80")}}, ""},
		{"trailing slash normalized", []PathRoute{toSite(prefix("/ords/"))}, ""},
		{"root", []PathRoute{toSite(prefix("/"))}, "paths[0].match.path"},
		{"no slash", []PathRoute{toSite(prefix("ords"))}, "paths[0].match.path"},
		{"wildcard", []PathRoute{toSite(prefix("/ords*"))}, "paths[0].match.path"},
		{"glob bracket", []PathRoute{toSite(exact("/[a]dmin"))}, "paths[0].match.path"},
		{"backslash", []PathRoute{toSite(prefix("/admin\\"))}, "paths[0].match.path"},
		{"placeholder", []PathRoute{toSite(prefix("/{env.X}"))}, "paths[0].match.path"},
		{"dot segment", []PathRoute{toSite(prefix("/a/../b"))}, "paths[0].match.path"},
		{"too long", []PathRoute{toSite(prefix("/" + strings.Repeat("a", 256)))}, "paths[0].match.path"},
		{"duplicate ignoring case", []PathRoute{toSite(prefix("/ORDS")), toSite(prefix("/ords"))}, "paths[1].match.path"},
		{"same path other type", []PathRoute{toSite(prefix("/ords")), toSite(exact("/ords"))}, ""},
		{"bad type", []PathRoute{toSite(PathMatch{Type: "regex", Path: "/x"})}, "paths[0].match.type"},
		{"bad action", []PathRoute{{Match: prefix("/x"), Action: "rewrite"}}, "paths[0].action"},
		{"strip exact", []PathRoute{{Match: exact("/x"), Action: ActionProxy, Target: PathTarget{Kind: TargetSite}, StripPrefix: true}}, "paths[0].strip_prefix"},
		{"bad kind", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: PathTarget{Kind: "swarm_service"}}}, "paths[0].target.kind"},
		{"site with upstreams", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: PathTarget{Kind: TargetSite, Upstreams: []string{"a:1"}}}}, "paths[0].target.kind"},
		{"static without upstreams", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: static()}}, "paths[0].target.upstreams"},
		{"admin api upstream", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: static("127.0.0.1:2019")}}, "paths[0].target.upstreams"},
		{"duplicate upstream", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: static("a:1", "A:1")}}, "paths[0].target.upstreams"},
		{"target tls key", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: PathTarget{Kind: TargetStatic, Upstreams: []string{"a:1"}, TLS: UpstreamTLS{TLS: true, CAPEM: testKeyPEM}}}}, "paths[0].target.tls.ca_pem"},
		{"header placeholder", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: PathTarget{Kind: TargetSite}, Headers: PathHeaders{RequestSet: []Header{{Name: "X", Value: "{file./etc/passwd}"}}}}}, "paths[0].headers.request_set"},
		{"secret response header", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: PathTarget{Kind: TargetSite}, Headers: PathHeaders{ResponseSet: []Header{{Name: "X", Value: "v", Secret: true}}}}}, "paths[0].headers.response_set"},
		{"bad timeout", []PathRoute{{Match: prefix("/x"), Action: ActionProxy, Target: PathTarget{Kind: TargetSite}, Transport: PathTransport{ReadTimeout: "1ms"}}}, "paths[0].transport.read_timeout"},
		{"redirect", []PathRoute{{Match: exact("/x"), Action: ActionRedirect, To: "/y", Code: 301}}, ""},
		{"redirect bad target", []PathRoute{{Match: exact("/x"), Action: ActionRedirect, To: "//evil.example", Code: 301}}, "paths[0].to"},
		{"redirect bad code", []PathRoute{{Match: exact("/x"), Action: ActionRedirect, To: "/y", Code: 303}}, "paths[0].code"},
		{"redirect with backend", []PathRoute{{Match: exact("/x"), Action: ActionRedirect, To: "/y", Code: 301, Target: PathTarget{Kind: TargetSite}}}, "paths[0].action"},
		{"respond", []PathRoute{{Match: prefix("/x"), Action: ActionRespond, Status: 403}}, ""},
		{"respond 500", []PathRoute{{Match: prefix("/x"), Action: ActionRespond, Status: 500}}, "paths[0].status"},
		{"respond with target", []PathRoute{{Match: prefix("/x"), Action: ActionRespond, Status: 404, To: "/y"}}, "paths[0].to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizePaths(tt.paths)
			err := validatePaths(tt.paths)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var pe *PathError
			if !errors.As(err, &pe) || !errors.Is(err, ErrInvalidOptions) || !strings.HasPrefix(err.Error(), tt.field+": ") {
				t.Fatalf("err = %v, want field %s", err, tt.field)
			}
		})
	}
	many := make([]PathRoute, maxPathRoutes+1)
	for i := range many {
		many[i] = toSite(prefix("/p" + strings.Repeat("x", i)))
	}
	if err := validatePaths(many); err == nil {
		t.Error("51 paths accepted")
	}
}

// Secret path header values are write-only like the site's: absent from
// API responses, the preview and audit, kept on a save that sends them
// back empty, and asked for again when the path gets another backend.
func TestPathSecretsWriteOnly(t *testing.T) {
	s := newServiceDB(t)
	ctx := context.Background()
	p := PathRoute{Match: prefix("/api"), Action: ActionProxy, Target: PathTarget{Kind: TargetStatic, Upstreams: []string{"10.0.0.30:80"}},
		Headers: PathHeaders{RequestSet: []Header{{Name: "Authorization", Value: "Bearer path-s3cret", Secret: true}}}}
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.5:80", TLSMode: "auto", Paths: []PathRoute{p}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(r)
	if strings.Contains(string(body), "path-s3cret") || !strings.Contains(string(body), `"value_set":true`) {
		t.Fatalf("create response: %s", body)
	}
	for _, mask := range []bool{false, true} {
		prev, err := s.SiteConfig(ctx, r.ID, mask)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := json.Marshal(prev)
		if strings.Contains(string(cfg), "path-s3cret") || !strings.Contains(string(cfg), maskedValue) {
			t.Errorf("preview (mask %v): %s", mask, cfg)
		}
	}
	masked := *r
	masked.Mask()
	if mb, _ := json.Marshal(masked); strings.Contains(string(mb), "path-s3cret") {
		t.Errorf("masked route: %s", mb)
	}

	sent := r.Paths
	changed, err := s.UpdateRoute(ctx, r.ID, r.Upstream, r.TLSMode, nil, nil, &sent)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Errorf("sending back what was returned changed %v", changed)
	}
	stored, _ := s.getRoute(ctx, r.ID)
	if stored.Paths[0].Headers.RequestSet[0].Value != "Bearer path-s3cret" {
		t.Fatalf("secret lost: %+v", stored.Paths[0].Headers.RequestSet)
	}

	moved := publicPaths(stored.Paths)
	moved[0].Target.Upstreams = []string{"10.0.0.99:80"}
	if _, err := s.UpdateRoute(ctx, r.ID, r.Upstream, r.TLSMode, nil, nil, &moved); err == nil || !strings.Contains(err.Error(), "again") {
		t.Fatalf("secret kept for a new backend: %v", err)
	}

	moved[0].Headers.RequestSet[0].Value = "Bearer other"
	changed, err = s.UpdateRoute(ctx, r.ID, r.Upstream, r.TLSMode, nil, nil, &moved)
	if err != nil || strings.Join(changed, ",") != "paths" {
		t.Fatalf("changed = %v, %v", changed, err)
	}
}

// A path to the site's backend keeps its secret only while the site
// gains no upstream, also when the paths are not sent.
func TestPathSecretsFollowSiteUpstream(t *testing.T) {
	s := newServiceDB(t)
	ctx := context.Background()
	p := toSite(prefix("/api"))
	p.Headers.RequestSet = []Header{{Name: "X-Key", Value: "path-s3cret", Secret: true}}
	r, err := s.CreateRoute(ctx, RouteSpec{Host: "app.example.com", Upstream: "10.0.0.5:80", TLSMode: "auto", Paths: []PathRoute{p}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRoute(ctx, r.ID, "10.0.0.6:80", r.TLSMode, nil, nil, nil); err == nil {
		t.Fatal("secret path header kept for a new site upstream")
	}
	if _, err := s.UpdateRoute(ctx, r.ID, r.Upstream, "internal", nil, nil, nil); err != nil {
		t.Fatalf("unrelated change refused: %v", err)
	}
}

// Rows without paths keep the state hash they had before the column
// existed, so an upgrade marks nothing as unloaded.
func TestStateHashIgnoresEmptyPaths(t *testing.T) {
	r := Route{Host: "a.example.com", Upstream: "10.0.0.1:80", TLSMode: "auto", Enabled: true, stored: "{}"}
	before := stateHash(&r)
	r.storedPaths = "[]"
	if stateHash(&r) != before {
		t.Fatal("empty paths changed the hash")
	}
	r.storedPaths = `[{"match":{"type":"prefix","path":"/x"}}]`
	if stateHash(&r) == before {
		t.Fatal("paths not in the hash")
	}
}

// A secret site header is not sent to a path's own backend, so naming a
// new address on a path can't hand it out.
func TestSiteSecretsStayWithSiteBackend(t *testing.T) {
	r := site("auto", SiteOptions{Headers: HeaderOptions{RequestSet: []Header{
		{Name: "Authorization", Value: "Bearer site-s3cret", Secret: true}, {Name: "X-Env", Value: "lab"}}}})
	r.Paths = []PathRoute{
		{Match: prefix("/x"), Action: ActionProxy, Target: PathTarget{Kind: TargetStatic, Upstreams: []string{"other.example.net:80"}}},
		toSite(prefix("/y")),
	}
	normalizePaths(r.Paths)
	sub := pathSubroute(r, portHTTPS)
	static, _ := json.Marshal(sub.Routes[0])
	own, _ := json.Marshal(sub.Routes[1])
	if !strings.Contains(string(static), "other.example.net") || strings.Contains(string(static), "site-s3cret") ||
		!strings.Contains(string(static), "X-Env") || !strings.Contains(string(static), `"delete":["Authorization"]`) {
		t.Errorf("static path: %s", static)
	}
	if !strings.Contains(string(own), "site-s3cret") {
		t.Errorf("site path lost the site's secret: %s", own)
	}
}

func TestSkipsVerifyCoversPaths(t *testing.T) {
	r := Route{Paths: []PathRoute{{Match: prefix("/x"), Action: ActionProxy,
		Target: PathTarget{Kind: TargetStatic, Upstreams: []string{"a:443"}, TLS: UpstreamTLS{TLS: true, SkipVerify: true}}}}}
	if !r.SkipsVerify() {
		t.Fatal("skip-verify on a path not reported")
	}
}
