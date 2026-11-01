package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

type ctxKey struct{ name string }

var (
	identityKey = ctxKey{"identity"}
	routePerms  = ctxKey{"routePerms"}
	resourceKey = ctxKey{"resource"}
)

// Identity is everything the auth layer knows about a caller. API-token
// callers have no UserID; their owner is in TokenOwnerID.
type Identity struct {
	UserID       string
	SessionID    string
	APITokenID   int64
	TokenOwnerID string
	// Access is evaluated from the user's (or the token owner's) current
	// assignments when the request comes in.
	Access *rbac.Access
}

// WithIdentity stores id in ctx. Used by NewAuth and by WebSocket handlers,
// which authenticate with a ticket instead of a header.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

func identity(ctx context.Context) Identity {
	id, _ := ctx.Value(identityKey).(Identity)
	return id
}

// SessionID is the session family of a user JWT, empty for API tokens.
func SessionID(ctx context.Context) string { return identity(ctx).SessionID }

// TokenOwnerID is the user an API token belongs to; empty for user
// sessions and legacy tokens.
func TokenOwnerID(ctx context.Context) string { return identity(ctx).TokenOwnerID }

// UserID is the authenticated user, empty for API tokens; callers that
// need an actor should fall back to TokenOwnerID or APITokenID.
func UserID(ctx context.Context) string { return identity(ctx).UserID }

// APITokenID is the API token of the request, 0 for user sessions.
func APITokenID(ctx context.Context) int64 { return identity(ctx).APITokenID }

// AccessOf returns the caller's evaluated access; never nil.
func AccessOf(ctx context.Context) *rbac.Access {
	if a := identity(ctx).Access; a != nil {
		return a
	}
	return &rbac.Access{}
}

// Allowed reports whether the caller holds perm on at least one scope.
// Handlers that act on a concrete host or stack must check that resource
// as well (see AllowedOn).
func Allowed(ctx context.Context, perm rbac.Perm) bool {
	return AccessOf(ctx).Allows(perm)
}

// AllowedOn reports whether the caller holds perm on res.
func AllowedOn(ctx context.Context, perm rbac.Perm, res rbac.Resource) bool {
	return AccessOf(ctx).AllowsOn(perm, res)
}

// RoutePerms returns the permissions the route's RequirePerm middleware
// checked, so scope checks in the handler test the same permissions on
// the resource the request acts on.
func RoutePerms(ctx context.Context) []rbac.Perm {
	v, _ := ctx.Value(routePerms).([]rbac.Perm)
	return v
}

// WithRoutePerm adds perm to the permissions scope checks test.
func WithRoutePerm(ctx context.Context, perm rbac.Perm) context.Context {
	return context.WithValue(ctx, routePerms, append(append([]rbac.Perm(nil), RoutePerms(ctx)...), perm))
}

// WithResource records the resource a route acts on (resolved by a
// middleware before routing reaches RequirePerm), so RequirePerm checks
// the permission there instead of anywhere.
func WithResource(ctx context.Context, res rbac.Resource) context.Context {
	return context.WithValue(ctx, resourceKey, res)
}

// ResourceOf returns the resource recorded by WithResource.
func ResourceOf(ctx context.Context) (rbac.Resource, bool) {
	res, ok := ctx.Value(resourceKey).(rbac.Resource)
	return res, ok
}

// APITokensStore is set by main.go at startup so the auth middleware
// can validate Bearer tokens with the "dmt_" prefix. If nil, only user
// JWTs are accepted.
var APITokensStore *apitokens.Service

// RBACStore is set by main.go at startup; it evaluates assignments.
var RBACStore *rbac.Store

// UserAccess evaluates the current assignments of a user.
func UserAccess(userID string) *rbac.Access {
	if RBACStore == nil {
		return &rbac.Access{Authenticated: true}
	}
	return RBACStore.AccessFor(userID)
}

// TokenIdentity resolves an API token to the caller identity. An owned
// token gets its owner's current access, narrowed to the token's role on
// the token's scope when it has one; a token whose owner is gone stops
// working. Legacy tokens (no owner on record) act as their role on the
// token's scope.
func TokenIdentity(ctx context.Context, svc *auth.Service, tok *apitokens.Token) (Identity, bool) {
	id := Identity{APITokenID: tok.ID}
	if RBACStore == nil {
		return id, false
	}
	scope := tok.Scope
	if scope.Type == "" {
		scope = rbac.Global
	}
	if tok.Legacy {
		perms, ok := RBACStore.RolePerms(tok.Role)
		if !ok {
			return id, false
		}
		id.Access = RBACStore.RoleAccess(tok.Role).Restrict(perms, scope)
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
	access := RBACStore.AccessFor(owner.ID)
	if tok.Role != "" {
		perms, ok := RBACStore.RolePerms(tok.Role)
		if !ok {
			perms = map[rbac.Perm]bool{}
		}
		access = access.Restrict(perms, scope)
	}
	id.Access = access
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
				id, ok := TokenIdentity(r.Context(), svc, tok)
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

			c, err := svc.Validate(r.Context(), token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), Identity{
				UserID:    c.UserID,
				SessionID: c.SessionID,
				Access:    UserAccess(c.UserID),
			})))
		})
	}
}

// RequirePerm rejects requests unless the caller holds perm: on the
// resource recorded by WithResource when there is one, otherwise on at
// least one scope. It records perm so the handler's scope checks test it
// on the concrete resource. Must be chained after NewAuth.
func RequirePerm(perm rbac.Perm) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok := Allowed(r.Context(), perm)
			if res, scoped := ResourceOf(r.Context()); scoped {
				ok = AllowedOn(r.Context(), perm, res)
			}
			if !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithRoutePerm(r.Context(), perm)))
		})
	}
}

func clientIP(r *http.Request) string { return clientip.IP(r) }
