package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/hosttags"
)

// A queued bulk update re-reads its caller before each item: a role or
// scope change, a sign-out or a revoked token stops the rest of the queue.
func TestQueuedUpdatesUseTheCallersCurrentRights(t *testing.T) {
	e := newAuthzEnv(t)
	ctx := context.Background()
	tags := hosttags.New(e.db)
	if err := tags.Load(ctx); err != nil {
		t.Fatal(err)
	}
	e.h.HostTags = tags

	u := e.user("ops", "host-admin")
	claims, err := e.h.Auth.Validate(ctx, e.login("ops").AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	userCtx := middleware.WithIdentity(ctx, middleware.Identity{UserID: u.ID, Access: e.h.Roles.AccessFor(u.ID), SessionID: claims.SessionID})

	e.user("boss", "admin")
	plain := e.apiToken(e.login("boss").AccessToken, "host-admin")
	tok, err := e.h.APITokens.Validate(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := middleware.TokenIdentity(ctx, e.h.Auth, tok)
	if !ok {
		t.Fatal("token identity")
	}
	tokenCtx := middleware.WithIdentity(ctx, id)

	denied := func(start context.Context) string {
		t.Helper()
		cur, err := e.h.refreshIdentity(start)
		if err != nil {
			return "caller gone"
		}
		return e.h.imageUpdateDenied(httptest.NewRequest("POST", "/", nil).WithContext(cur), "abc")
	}

	steps := []struct {
		name   string
		change func()
		start  context.Context
		want   string
	}{
		{"host admin may update", func() {}, userCtx, ""},
		{"scope that leaves out the server", func() { e.scopeUser(u.ID, "prod") }, userCtx, "the container is outside your scope"},
		{"scope removed again", func() { e.scopeUser(u.ID) }, userCtx, ""},
		{"demoted to viewer", func() { e.setRole(u.ID, "viewer") }, userCtx, "needs the containers.upgrade permission"},
		{"signed out", func() {
			if _, err := e.h.Auth.RevokeUserSessions(ctx, u.ID, ""); err != nil {
				t.Fatal(err)
			}
		}, userCtx, "caller gone"},
		{"api token may update", func() {}, tokenCtx, ""},
		{"api token revoked", func() {
			if err := e.h.APITokens.Revoke(ctx, tok.ID); err != nil {
				t.Fatal(err)
			}
		}, tokenCtx, "caller gone"},
	}
	for _, s := range steps {
		s.change()
		if got := denied(s.start); got != s.want {
			t.Fatalf("%s: got %q, want %q", s.name, got, s.want)
		}
	}
}
