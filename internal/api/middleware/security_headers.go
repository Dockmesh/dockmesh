package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/dockmesh/dockmesh/internal/clientip"
)

// inlineScriptRe matches inline <script> elements (no src attribute) in the
// SvelteKit build output, which is generated and predictable enough for a
// regexp.
var inlineScriptRe = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)

// ScriptHash returns the CSP source expression allowing exactly this inline
// script body.
func ScriptHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// InlineScriptHashes collects the hashes of every inline script in the HTML
// files of the embedded UI. SvelteKit boots the SPA from an inline script
// whose content changes per build, so the CSP allows it by hash instead of
// 'unsafe-inline'.
func InlineScriptHashes(webFS fs.FS) []string {
	if webFS == nil {
		return nil
	}
	seen := map[string]bool{}
	// An unreadable file only means its scripts stay blocked; the walk
	// carries on so one bad entry can't drop every hash.
	_ = fs.WalkDir(webFS, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil //nolint:nilerr
		}
		b, readErr := fs.ReadFile(webFS, p)
		if readErr != nil {
			return nil //nolint:nilerr
		}
		for _, m := range inlineScriptRe.FindAllStringSubmatch(string(b), -1) {
			if strings.Contains(strings.ToLower(m[1]), "src=") {
				continue
			}
			seen[ScriptHash(m[2])] = true
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// ContentSecurityPolicy builds the policy for the UI. Styles keep
// 'unsafe-inline' because Svelte sets style attributes and transition
// styles at runtime; scripts never do.
func ContentSecurityPolicy(scriptHashes []string) string {
	script := "script-src 'self'"
	if len(scriptHashes) > 0 {
		script += " " + strings.Join(scriptHashes, " ")
	}
	return strings.Join([]string{
		"default-src 'self'",
		script,
		"style-src 'self' 'unsafe-inline'",
		// Template icons are arbitrary https URLs; the TOTP QR code is a data URL.
		"img-src 'self' data: blob: https:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// SecurityHeaders sets the browser hardening headers on every response.
// Handlers that serve their own HTML (Swagger UI) may replace the CSP.
// HSTS is only sent when the client reached us over TLS, directly or via a
// trusted proxy; on plain HTTP it would be ignored at best.
func SecurityHeaders(csp string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Frame-Options", "DENY")
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			if clientip.Secure(r) {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}
