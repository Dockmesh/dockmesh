package middleware

import (
	"context"
	"errors"
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
	ownerKey      = ctxKey{"tokenOwner"}
	ownerRoleKey  = ctxKey{"tokenOwnerRole"}
	sessionIDKey  = ctxKey{"sessionID"}
)

// Identity is everything the auth layer knows about a caller. API-token
// callers have no UserID; their owner is in TokenOwnerID.
type Identity struct {
	UserID       string
	Role         string
	ScopeTags    []string
	SessionID    string
	APITokenID   int64
	TokenOwnerID string
	// TokenOwnerRole is the owner's current role. Permission and role-scope
	// checks must pass for Role and for TokenOwnerRole.
	TokenOwnerRole string
}

// WithIdentity stores id in ctx. Used by NewAuth and by WebSocket handlers,
// which authenticate with a ticket instead of a header.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	ctx = context.WithValue(ctx, userIDKey, id.UserID)
	ctx = context.WithValue(ctx, roleKey, id.Role)
	ctx = context.WithValue(ctx, scopeTagsKey, id.ScopeTags)
	ctx = context.WithValue(ctx, sessionIDKey, id.SessionID)
	ctx = context.WithValue(ctx, apiTokenIDKey, id.APITokenID)
	ctx = context.WithValue(ctx, ownerKey, id.TokenOwnerID)
	return context.WithValue(ctx, ownerRoleKey, id.TokenOwnerRole)
}

// SessionID is the session family of a user JWT, empty for API tokens.
func SessionID(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDKey).(string)
	return v
}

// TokenOwnerID is the user an API token belongs to; empty for user
// sessions and legacy tokens.
func TokenOwnerID(ctx context.Context) string {
	v, _ := ctx.Value(ownerKey).(string)
	return v
}

// TokenOwnerRole is the current role of the API token's owner.
func TokenOwnerRole(ctx context.Context) string {
	v, _ := ctx.Value(ownerRoleKey).(string)
	return v
}

// Roles returns every role a permission or role-scope check has to pass:
// the caller's role, plus the owner's role for owned API tokens.
func Roles(ctx context.Context) []string {
	roles := []string{Role(ctx)}
	if owner := TokenOwnerRole(ctx); owner != "" {
		roles = append(roles, owner)
	}
	return roles
}

func roleAllowed(role string, perm rbac.Perm) bool {
	if RBACStore != nil {
		return RBACStore.AllowedDB(role, perm)
	}
	return rbac.Allowed(role, perm)
}

// Allowed reports whether the caller holds perm under every role in Roles.
func Allowed(ctx context.Context, perm rbac.Perm) bool {
	for _, role := range Roles(ctx) {
		if !roleAllowed(role, perm) {
			return false
		}
	}
	return true
}

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
// token at creation time; see Roles for the owner's role.
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

// tokenIdentity resolves an API token to the caller identity. An owned
// token gets its owner's current scope and is checked against the owner's
// current role as well; a token whose owner is gone stops working. Legacy
// tokens (no owner on record) keep their pinned role and no scope.
func tokenIdentity(ctx context.Context, svc *auth.Service, tok *apitokens.Token) (Identity, bool) {
	id := Identity{Role: tok.Role, APITokenID: tok.ID}
	if tok.Legacy {
		return id, true
	}
	if tok.CreatedBy == nil {
		return id, false
	}
	owner, err := svc.GetUser(ctx, *tok.CreatedBy)
	if err != nil {
		return id, false
	}
	id.TokenOwnerID = owner.ID
	id.TokenOwnerRole = owner.Role
	id.ScopeTags = owner.ScopeTags
	return id, true
}

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
				if errors.Is(err, apitokens.ErrBusy) {
					w.Header().Set("Retry-After", "1")
					http.Error(w, "too many token checks in flight", http.StatusServiceUnavailable)
					return
				}
				if err != nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				id, ok := tokenIdentity(r.Context(), svc, tok)
				if !ok {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				// Record usage for last-used-at tracking. Buffered,
				// not a per-request DB write.
				APITokensStore.TouchAsync(tok.ID, clientIP(r))
				next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
				return
			}

			// User JWT path.
			c, err := svc.Validate(r.Context(), token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), Identity{
				UserID:    c.UserID,
				Role:      c.Role,
				ScopeTags: c.ScopeTags,
				SessionID: c.SessionID,
			})))
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
			if !Allowed(r.Context(), perm) {
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
