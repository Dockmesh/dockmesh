package proxy

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const maxURLPathLen = 256

// checkURLPath is the check for every URL path Dockmesh writes into the
// Caddy config. Caddy URL-parses such paths when it provisions the
// config and refuses the whole config over one bad percent escape or
// control character, so they are caught here first. Dot segments are
// refused too: Caddy cleans the request path before it matches, so a
// path with them never means what it says.
func checkURLPath(p string) error {
	switch {
	case p == "":
		return errors.New("is empty")
	case !strings.HasPrefix(p, "/"):
		return errors.New("must start with /")
	case len(p) > maxURLPathLen:
		return fmt.Errorf("is longer than %d characters", maxURLPathLen)
	case strings.ContainsAny(p, " #?"):
		return errors.New("must not contain spaces, # or ?")
	case hasPlaceholderOrControl(p):
		return errors.New("must not contain braces or control characters")
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && (i+2 >= len(p) || !isHex(p[i+1]) || !isHex(p[i+2])) {
			return errors.New("has a % that is not followed by two hex digits; write a literal % as %25")
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return errors.New("must not contain . or .. segments")
		}
	}
	if _, err := url.ParseRequestURI(p); err != nil {
		return errors.New("is not a valid URL path")
	}
	return nil
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// checkChangedPaths runs checkURLPath on every path in o that differs
// from stored, the site's saved options (nil for a new site). A stored
// path that fails the check keeps working until it is changed.
func (o *SiteOptions) checkChangedPaths(stored *SiteOptions) error {
	if hp := o.Routing.HealthPath; hp != "" && (stored == nil || hp != stored.Routing.HealthPath) {
		path, _, _ := strings.Cut(hp, "?")
		if err := checkURLPath(path); err != nil {
			return optErr("routing.health_path", "%v", err)
		}
	}
	if f := o.Access.ForwardAuth; f != nil {
		path := forwardAuthPath(f)
		var old string
		if stored != nil && stored.Access.ForwardAuth != nil {
			old = forwardAuthPath(stored.Access.ForwardAuth)
		}
		if path != "/" && (stored == nil || path != old) {
			if err := checkURLPath(path); err != nil {
				return optErr("access.forward_auth.url", "path %v", err)
			}
		}
	}
	return nil
}

// forwardAuthPath is the escaped path of a forward-auth URL, "/" when it
// has none and "" when the URL doesn't parse.
func forwardAuthPath(f *ForwardAuth) string {
	u, err := url.Parse(f.URL)
	if err != nil {
		return ""
	}
	if p := u.EscapedPath(); p != "" {
		return p
	}
	return "/"
}
