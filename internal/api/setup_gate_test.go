package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// openBeforeCode is everything that may answer in setup mode before a
// setup code opened a session. Adding to it needs a reason.
var openBeforeCode = map[string]bool{
	"GET /api/v1/setup/status": true,
	"POST /api/v1/setup/code":  true,
	"GET /api/v1/health":       true,
	"GET /healthz/live":        true,
	"GET /healthz/ready":       true,
}

func setupModeRouter(t *testing.T) (http.Handler, *setup.State, string) {
	t.Helper()
	codeFile := filepath.Join(t.TempDir(), "setup-code.hash")
	st := setup.NewForced(codeFile)
	h := handlers.New(handlers.Deps{Auth: (*auth.Service)(nil), SetupState: st})
	web := fstest.MapFS{
		"index.html":          {Data: []byte("<html></html>")},
		"_app/immutable/a.js": {Data: []byte("x")},
		"favicon.ico":         {Data: []byte("x")},
	}
	return NewRouter(h, nil, fs.FS(web), false, st), st, codeFile
}

// serve runs one request; a handler that panics on its nil dependencies
// has passed the gate, which never panics itself.
func serve(router http.Handler, req *http.Request) (rec *httptest.ResponseRecorder, passed bool) {
	rec = httptest.NewRecorder()
	defer func() {
		if recover() != nil {
			passed = true
		}
	}()
	router.ServeHTTP(rec, req)
	return rec, false
}

func gateRefused(rec *httptest.ResponseRecorder) bool {
	switch rec.Code {
	case http.StatusSeeOther:
		return rec.Header().Get("Location") == "/setup"
	case http.StatusUnauthorized, http.StatusServiceUnavailable:
		var body struct {
			SetupURL string `json:"setup_url"`
		}
		return json.Unmarshal(rec.Body.Bytes(), &body) == nil && body.SetupURL == "/setup"
	}
	return false
}

func concretePath(route string) string {
	parts := strings.Split(route, "/")
	for i, p := range parts {
		switch {
		case p == "*":
			parts[i] = "anything"
		case strings.HasPrefix(p, "{"):
			parts[i] = "x"
		}
	}
	return strings.Join(parts, "/")
}

// Every registered route, UI and API, is refused in setup mode without a
// setup session, except openBeforeCode. A route added later is covered
// without touching this test.
func TestSetupModeRefusesEveryRouteWithoutSession(t *testing.T) {
	router, _, _ := setupModeRouter(t)
	n := 0
	err := chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		n++
		key := method + " " + route
		for _, token := range []string{"", "forged"} {
			req := httptest.NewRequest(method, concretePath(route), strings.NewReader("{}"))
			req.Header.Set(middleware.SetupHeader, "1")
			if token != "" {
				req.AddCookie(&http.Cookie{Name: middleware.SetupCookie, Value: token})
			}
			rec, passed := serve(router, req)
			refused := !passed && gateRefused(rec)
			if openBeforeCode[key] && refused {
				t.Errorf("%s refused before the code, want it open", key)
			}
			if !openBeforeCode[key] && !refused {
				t.Errorf("%s answered %d without a setup session (token %q)", key, rec.Code, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Fatalf("walked only %d routes", n)
	}
}

func TestSetupModeUIRoutesWithoutSession(t *testing.T) {
	router, _, _ := setupModeRouter(t)
	tests := []struct {
		path string
		open bool
	}{
		{path: "/setup", open: true},
		{path: "/_app/immutable/a.js", open: true},
		{path: "/favicon.ico", open: true},
		{path: "/"},
		{path: "/login"},
		{path: "/stacks"},
		{path: "/settings/system"},
		{path: "/setup/anything"},
		{path: "/api/v1/openapi.json"},
		{path: "/api/v1/docs"},
		{path: "/api/v1/setup/preflight"},
		{path: "/api/v1/setup/server-info"},
		{path: "/api/v1/stacks/x.js"},
		{path: "/ws/logs/x.js"},
		{path: "/ws/exec/x.js"},
		{path: "/install/x.js"},
		{path: "/robots.txt", open: true},
		{path: "/metrics"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec, passed := serve(router, httptest.NewRequest(http.MethodGet, tt.path, nil))
			refused := !passed && gateRefused(rec)
			if refused == tt.open {
				t.Fatalf("GET %s: %d, refused=%v, want open=%v", tt.path, rec.Code, refused, tt.open)
			}
		})
	}
}

func TestSetupSessionOpensOnlySetupAPI(t *testing.T) {
	router, st, codeFile := setupModeRouter(t)
	code, err := setup.NewCode()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := setup.NewCodeRecord(code, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(codeFile, data, 0o640); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/code", strings.NewReader(`{"code":"`+setup.FormatCode(code)+`"}`))
	req.Header.Set(middleware.SetupHeader, "1")
	res, _ := serve(router, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("code: %d %s", res.Code, res.Body)
	}
	var cookie *http.Cookie
	for _, c := range res.Result().Cookies() {
		if c.Name == middleware.SetupCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("setup cookie = %+v", cookie)
	}
	if st.Codes().Session(cookie.Value) != setup.SessionWrite {
		t.Fatal("cookie does not carry a setup session")
	}
	tests := []struct {
		method, path string
		open         bool
	}{
		{method: http.MethodPost, path: "/api/v1/setup/validate-data-dir", open: true},
		{method: http.MethodGet, path: "/api/v1/setup/preflight", open: true},
		{method: http.MethodGet, path: "/api/v1/stacks"},
		{method: http.MethodPost, path: "/api/v1/auth/login"},
		{method: http.MethodGet, path: "/"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{"path":"/srv/dm"}`))
		req.Header.Set(middleware.SetupHeader, "1")
		req.AddCookie(cookie)
		rec, passed := serve(router, req)
		refused := !passed && gateRefused(rec)
		if refused == tt.open {
			t.Errorf("%s %s with a session: %d, refused=%v, want open=%v", tt.method, tt.path, rec.Code, refused, tt.open)
		}
	}
}
