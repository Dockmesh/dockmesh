package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

type ctxKey struct{ name string }

var (
	userIDKey = ctxKey{"userID"}
	roleKey   = ctxKey{"role"}
)

// UserID extracts the authenticated user ID from the request context.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// Role extracts the authenticated user's role from the request context.
func Role(ctx context.Context) string {
	v, _ := ctx.Value(roleKey).(string)
	return v
}

// NewAuth returns middleware that validates Bearer JWTs via the auth service
// and injects the user ID + role into the request context.
func NewAuth(svc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(h, "Bearer ")
			uid, role, err := svc.Validate(token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, uid)
			ctx = context.WithValue(ctx, roleKey, role)
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
