package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

func gitCreateEnv(t *testing.T) (*authzEnv, *stacks.Manager) {
	t.Helper()
	e := newScopeEnv(t)
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	e.h.Stacks = mgr
	e.h.GitSource = gitsource.New(e.db, nil, mgr, t.TempDir(), nil)
	e.router.Group(func(r chi.Router) {
		r.Use(middleware.NewAuth(e.h.Auth))
		r.With(middleware.RequirePerm(rbac.PermStacksCreate)).Post("/from-git", e.h.CreateStackFromGit)
	})
	e.user("root", "admin")
	return e, mgr
}

// A failed first sync leaves nothing that a later webhook could turn into
// a deployed stack, and the name can be used again.
func TestFailedInitialGitSyncFreesTheName(t *testing.T) {
	e, mgr := gitCreateEnv(t)
	ctx := context.Background()
	root := e.login("root").AccessToken
	body := map[string]any{"name": "web", "host_id": "local", "git": map[string]any{"repo_url": "https://127.0.0.1:1/none.git"}}

	for attempt := 1; attempt <= 2; attempt++ {
		rr := e.do(http.MethodPost, "/from-git", root, body)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d: %d, want 422: %s", attempt, rr.Code, rr.Body)
		}
	}
	if _, err := e.h.GitSource.Get(ctx, "web"); !errors.Is(err, gitsource.ErrNotFound) {
		t.Errorf("git source kept: %v", err)
	}
	if got, _ := e.h.Deployments.Target(ctx, "web"); got != "" {
		t.Errorf("target host kept: %q", got)
	}
	if mgr.Has("web") {
		t.Error("stack exists after the failed sync")
	}
}

func TestRollBackGitCreateRemovesPartialStack(t *testing.T) {
	e, mgr := gitCreateEnv(t)
	ctx := context.Background()
	if _, err := mgr.Create("partial", "services: {}\n", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.h.GitSource.Configure(ctx, "partial", gitsource.Input{RepoURL: "https://example.com/p.git"}); err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/from-git", nil).WithContext(gone)
	e.h.rollBackGitCreate(r, "partial")
	if mgr.Has("partial") {
		t.Error("partly copied stack kept")
	}
	if _, err := e.h.GitSource.Get(ctx, "partial"); !errors.Is(err, gitsource.ErrNotFound) {
		t.Errorf("git source kept after the client went away: %v", err)
	}
}
