package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// roleAllows checks perm for the caller's role, custom roles included.
func roleAllows(r *http.Request, perm rbac.Perm) bool {
	role := middleware.Role(r.Context())
	if middleware.RBACStore != nil {
		return middleware.RBACStore.AllowedDB(role, perm)
	}
	return rbac.Allowed(role, perm)
}
