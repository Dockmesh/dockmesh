package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/auth"
)

type ctxKey struct{ name string }

var userIDKey = ctxKey{"userID"}

// UserID extracts the authenticated user ID from the request context.
// Returns "" if the request did not pass through NewAuth.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// NewAuth returns middleware that validates Bearer JWTs via the auth service
// and injects the user ID into the request context.
func NewAuth(svc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(h, "Bearer ")
			uid, err := svc.Validate(token)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, uid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
