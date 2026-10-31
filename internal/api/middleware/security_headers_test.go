package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dockmesh/dockmesh/internal/clientip"
)

func TestInlineScriptHashes(t *testing.T) {
	boot := "\n\t\t\t\t{ kit.start(app, element); }\n\t\t\t"
	webFS := fstest.MapFS{
		"index.html":       {Data: []byte(`<html><head><script type="module" src="/_app/x.js"></script></head><body><div><script>` + boot + `</script></div></body></html>`)},
		"_app/x.js":        {Data: []byte(`<script>not html</script>`)},
		"setup/index.html": {Data: []byte(`<script>` + boot + `</script>`)},
	}
	got := InlineScriptHashes(webFS)
	if len(got) != 1 || got[0] != ScriptHash(boot) {
		t.Fatalf("hashes = %v, want only the inline boot script", got)
	}
	// Known vector: sha256("") in base64.
	if h := ScriptHash(""); h != "'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='" {
		t.Fatalf("ScriptHash(\"\") = %s", h)
	}
}

func TestSecurityHeaders(t *testing.T) {
	csp := ContentSecurityPolicy([]string{"'sha256-abc'"})
	for _, want := range []string{"script-src 'self' 'sha256-abc'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(strings.SplitN(strings.SplitN(csp, "script-src", 2)[1], ";", 2)[0], "unsafe-inline") {
		t.Errorf("script-src allows unsafe-inline: %q", csp)
	}

	resolver := clientip.New(func() string { return "10.0.0.2" })
	tests := []struct {
		name     string
		remote   string
		proto    string
		wantHSTS bool
	}{
		{name: "plain http", remote: "203.0.113.1:1"},
		{name: "forged proto", remote: "203.0.113.1:1", proto: "https"},
		{name: "tls at trusted proxy", remote: "10.0.0.2:1", proto: "https", wantHSTS: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			if tt.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			rr := httptest.NewRecorder()
			resolver.Middleware(SecurityHeaders(csp)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))).ServeHTTP(rr, req)
			h := rr.Header()
			for k, v := range map[string]string{
				"Content-Security-Policy": csp,
				"X-Frame-Options":         "DENY",
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "same-origin",
			} {
				if h.Get(k) != v {
					t.Errorf("%s = %q, want %q", k, h.Get(k), v)
				}
			}
			if got := h.Get("Strict-Transport-Security") != ""; got != tt.wantHSTS {
				t.Errorf("HSTS sent = %v, want %v", got, tt.wantHSTS)
			}
		})
	}
}
