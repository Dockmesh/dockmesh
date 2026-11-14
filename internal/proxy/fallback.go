package proxy

import (
	"net/url"
	"strings"
)

func (f *Fallback) validate(field string) error {
	switch f.Mode {
	case "", FallbackProxy, FallbackNotFound:
		if f.To != "" || f.Code != 0 {
			return optErr(field+".to", "only a redirect has a target")
		}
		return nil
	case FallbackRedirect:
		return checkRedirect(field, f.To, f.Code)
	}
	return optErr(field+".mode", "must be proxy, redirect or 404")
}

var redirectCodes = map[int]bool{301: true, 302: true, 307: true, 308: true}

// checkRedirect accepts an absolute path (with an optional query) or an
// http(s) URL, and one of the redirect status codes. Caddy expands
// placeholders in the Location header, so braces are refused.
func checkRedirect(field, to string, code int) error {
	if !redirectCodes[code] {
		return optErr(field+".code", "must be 301, 302, 307 or 308")
	}
	bad := func() error { return optErr(field+".to", "use a path starting with / or an http(s) URL") }
	if to == "" || len(to) > 2048 || hasPlaceholderOrControl(to) || strings.ContainsAny(to, " \\") {
		return bad()
	}
	if strings.HasPrefix(to, "/") {
		path, _, _ := strings.Cut(to, "?")
		path, _, _ = strings.Cut(path, "#")
		if err := checkURLPath(path); err != nil {
			return optErr(field+".to", "path %v", err)
		}
		return nil
	}
	u, err := url.Parse(to)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return bad()
	}
	return nil
}
