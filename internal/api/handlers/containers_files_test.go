package handlers

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Container file routes check the files extra (and, for writes, the
// terminal extra) on the container's host, not only on any assignment.
func TestContainerFilesNeedFilesOnTheContainer(t *testing.T) {
	e := newScopeEnv(t)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.Use(middleware.RequirePerm(rbac.PermContainersFiles))
		r.Get("/containers/{id}/files", e.h.BrowseContainerFiles)
		r.With(middleware.RequirePerm(rbac.PermContainersExec)).Post("/containers/{id}/files", e.h.WriteContainerFile)
		r.With(middleware.RequirePerm(rbac.PermContainersExec)).Delete("/containers/{id}/files", e.h.DeleteContainerFile)
	})
	view := rbac.PermContainersView
	local := rbac.Scope{Type: rbac.ScopeHost, Host: "local"}
	e.role("files-rw", []rbac.Perm{view, rbac.PermContainersFiles, rbac.PermContainersExec}, local)
	e.role("files-ro", []rbac.Perm{view, rbac.PermContainersFiles}, local)
	e.role("terminal-only", []rbac.Perm{view, rbac.PermContainersExec}, local)
	e.role("files-elsewhere", []rbac.Perm{view, rbac.PermContainersFiles, rbac.PermContainersExec}, rbac.Scope{Type: rbac.ScopeHost, Host: "agent-1"})
	e.role("files-on-stack", []rbac.Perm{view, rbac.PermContainersFiles, rbac.PermContainersExec}, rbac.Scope{Type: "stack", Value: "web"})
	for _, u := range []string{"files-rw", "files-ro", "terminal-only", "files-elsewhere", "files-on-stack"} {
		e.user(u, u)
	}
	write := map[string]string{"path": "/etc/x", "content": "eA=="}

	// No Docker in tests: a request that passes every check fails with 503.
	tests := []struct {
		user, method, path string
		want               int
	}{
		{"files-rw", http.MethodGet, "/containers/c1/files?path=/", http.StatusServiceUnavailable},
		{"files-rw", http.MethodPost, "/containers/c1/files", http.StatusServiceUnavailable},
		{"files-rw", http.MethodDelete, "/containers/c1/files?path=/etc/x", http.StatusServiceUnavailable},
		{"files-ro", http.MethodGet, "/containers/c1/files?path=/", http.StatusServiceUnavailable},
		{"files-ro", http.MethodPost, "/containers/c1/files", http.StatusForbidden},
		{"files-ro", http.MethodDelete, "/containers/c1/files?path=/etc/x", http.StatusForbidden},
		{"terminal-only", http.MethodGet, "/containers/c1/files?path=/", http.StatusForbidden},
		{"terminal-only", http.MethodPost, "/containers/c1/files", http.StatusForbidden},
		{"files-elsewhere", http.MethodGet, "/containers/c1/files?path=/", http.StatusForbidden},
		{"files-elsewhere", http.MethodPost, "/containers/c1/files", http.StatusForbidden},
		{"files-on-stack", http.MethodGet, "/containers/c1/files?path=/", http.StatusForbidden},
		{"files-on-stack", http.MethodDelete, "/containers/c1/files?path=/etc/x", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.user+" "+tt.method, func(t *testing.T) {
			var body any
			if tt.method == http.MethodPost {
				body = write
			}
			rr := e.do(tt.method, tt.path, e.login(tt.user).AccessToken, body)
			if rr.Code != tt.want {
				t.Errorf("%d, want %d: %s", rr.Code, tt.want, rr.Body)
			}
		})
	}
}
