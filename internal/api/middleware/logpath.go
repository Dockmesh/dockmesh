package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// secretSegment is the path segment after which an invite token follows,
// both in /api/v1/invite/{token}[/accept] and in the page /invite/{token}.
const secretSegment = "invite"

const redactedSegment = "[redacted]"

// RedactPath returns p with the invite token replaced, for logs, audit
// entries and traces. It goes by segment rather than by the matched
// route, so a mistyped or extra-slashed path that ends in a 404 is
// covered as well; every segment after "invite" except "accept" counts
// as part of the token.
func RedactPath(p string) string {
	if !strings.Contains(strings.ToLower(p), secretSegment) {
		return p
	}
	segs := strings.Split(p, "/")
	inToken := false
	for i, s := range segs {
		switch {
		case strings.EqualFold(s, secretSegment):
			inToken = true
		case inToken && s != "" && s != "accept":
			segs[i] = redactedSegment
		}
	}
	return strings.Join(segs, "/")
}

type realURLKey struct{}

// HidePathSecrets hands wrap, a tracing middleware that records the
// request path, a request whose path went through RedactPath; next still
// sees the real one.
func HidePathSecrets(next http.Handler, wrap func(http.Handler) http.Handler) http.Handler {
	inner := wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := r.Context().Value(realURLKey{}).(*url.URL); ok {
			r = r.WithContext(r.Context())
			r.URL = u
		}
		next.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hidden := RedactPath(r.URL.Path)
		if hidden == r.URL.Path {
			inner.ServeHTTP(w, r)
			return
		}
		u := *r.URL
		u.Path, u.RawPath = hidden, ""
		r2 := r.WithContext(context.WithValue(r.Context(), realURLKey{}, r.URL))
		r2.URL = &u
		inner.ServeHTTP(w, r2)
	})
}
