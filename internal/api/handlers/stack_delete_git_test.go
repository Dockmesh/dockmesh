package handlers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// A git source left behind would re-create a deleted stack on the next
// commit, so deleting the stack drops the source and its clone.
func TestDeleteStackRemovesGitSource(t *testing.T) {
	e := newScopeEnv(t)
	ctx := context.Background()
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	e.h.Stacks = mgr
	cache := t.TempDir()
	e.h.GitSource = gitsource.New(e.db, nil, mgr, cache, nil)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksDelete)).Delete("/stacks/{name}", e.h.DeleteStack)
	})
	e.user("root", "admin")
	root := e.login("root").AccessToken

	for _, name := range []string{"web", "plain"} {
		if _, err := mgr.Create(name, "services:\n  a:\n    image: busybox\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.h.GitSource.Configure(ctx, "web", gitsource.Input{RepoURL: "https://example.com/web.git"}); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(cache, "web")
	if err := os.MkdirAll(clone, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"web", "plain"} {
		if rr := e.do(http.MethodDelete, "/stacks/"+name, root, nil); rr.Code != http.StatusNoContent {
			t.Fatalf("delete %s: %d %s", name, rr.Code, rr.Body)
		}
	}
	if _, err := e.h.GitSource.Get(ctx, "web"); !errors.Is(err, gitsource.ErrNotFound) {
		t.Errorf("git source kept after the stack was deleted: %v", err)
	}
	if _, err := os.Stat(clone); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("clone cache kept: %v", err)
	}
}
