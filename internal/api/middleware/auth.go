package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

type ctxKey struct{ name string }

var (
	userIDKey     = ctxKey{"userID"}
	roleKey       = ctxKey{"role"}
	scopeTagsKey  = ctxKey{"scopeTags"}
	apiTokenIDKey = ctxKey{"apiTokenID"}
)

// UserID extracts the authenticated user ID from the request context.
// For API-token-authenticated requests, returns empty string (no user
// session) — callers that need an actor identifier should prefer the
// role combined with APITokenID.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// Role extracts the authenticated user's role from the request context.
// For API-token-authenticated requests, returns the role pinned to the
// token at creation time.
func Role(ctx context.Context) string {
	v, _ := ctx.Value(roleKey).(string)
	return v
}

// APITokenID returns the database id of the API token used to
// authenticate this request, or 0 for JWT-authenticated user sessions.
// Useful for audit logs that want to distinguish "alice logged in and
// did X" from "the github-actions token did X".
func APITokenID(ctx context.Context) int64 {
	v, _ := ctx.Value(apiTokenIDKey).(int64)
	return v
}

// ScopeTags returns the caller's host-tag scope. Empty / nil means the
// caller has access to all hosts. Non-empty narrows access to hosts
// whose host_tags intersect with the scope (OR match). Handlers that
// act on a specific host should call Handlers.canAccessHost() which
// combines this with the hosttags service.
func ScopeTags(ctx context.Context) []string {
	v, _ := ctx.Value(scopeTagsKey).([]string)
	return v
}

// APITokensStore is set by main.go at startup so the auth middleware
// can validate Bearer tokens with the "dmt_" prefix. If nil, only user
// JWTs are accepted.
var APITokensStore *apitokens.Service

// NewAuth returns middleware that validates Bearer tokens and injects
// the caller identity into the request context.
//
// Accepts two token shapes:
//   - User JWT (short-lived access token minted by auth.Service)
//   - API token prefixed "dmt_" (created via Settings → API tokens)
//
// The prefix makes it a cheap O(1) decision which code path to take.
func NewAuth(svc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(h, "Bearer ")

			// API token path — only if the store is configured.
			if strings.HasPrefix(token, apitokens.TokenPrefix) {
				if APITokensStore == nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				tok, err := APITokensStore.Validate(r.Context(), token)
				if err != nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				// Record usage for last-used-at tracking. Buffered,
				// not a per-request DB write.
				APITokensStore.TouchAsync(tok.ID, clientIP(r))
				ctx := context.WithValue(r.Context(), roleKey, tok.Role)
				ctx = context.WithValue(ctx, apiTokenIDKey, tok.ID)
				// userID deliberately left empty — no user session here.
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// User JWT path.
			uid, role, scope, err := svc.Validate(token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, uid)
			ctx = context.WithValue(ctx, roleKey, role)
			ctx = context.WithValue(ctx, scopeTagsKey, scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole returns middleware that rejects requests whose user does not
// have any of the allowed roles. Must be chained after NewAuth.
func RequireRole(allowed ...string) func(http.Handler) http.Handler {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, r := range allowed {
		allowedSet[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := Role(r.Context())
			if _, ok := allowedSet[role]; !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RBACStore is set by main.go at startup so the middleware can use
// DB-backed custom roles. If nil, falls back to hardcoded builtins.
var RBACStore *rbac.Store

// RequirePerm returns middleware that rejects requests whose user role
// is not granted the given permission. Must be chained after NewAuth.
func RequirePerm(perm rbac.Perm) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := Role(r.Context())
			allowed := false
			if RBACStore != nil {
				allowed = RBACStore.AllowedDB(role, perm)
			} else {
				allowed = rbac.Allowed(role, perm)
			}
			if !allowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP best-effort extracts the caller's IP. Prefers X-Forwarded-For
// when present (behind a reverse proxy), falls back to RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry in the comma-separated list is the original client.
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	// RemoteAddr is "ip:port" — strip the port.
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}
