package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

var errCallerGone = errors.New("the session or API token that started it is no longer valid")

// refreshIdentity re-reads the caller of ctx: the user's current role and
// scope while the session is live, or the API token's current owner while
// the token is neither revoked nor expired. Queued work started by a
// request uses it so a later role, scope or sign-out change applies.
func (h *Handlers) refreshIdentity(ctx context.Context) (context.Context, error) {
	if tid := middleware.APITokenID(ctx); tid != 0 {
		if h.APITokens == nil {
			return ctx, errCallerGone
		}
		tok, err := h.APITokens.Get(ctx, tid)
		if err != nil || tok.RevokedAt != nil || (tok.ExpiresAt != nil && tok.ExpiresAt.Before(time.Now())) {
			return ctx, errCallerGone
		}
		id, ok := middleware.TokenIdentity(ctx, h.Auth, tok)
		if !ok {
			return ctx, errCallerGone
		}
		return middleware.WithIdentity(ctx, id), nil
	}
	uid, sid := middleware.UserID(ctx), middleware.SessionID(ctx)
	if err := h.Auth.SessionLive(ctx, uid, sid); err != nil {
		return ctx, errCallerGone
	}
	return middleware.WithIdentity(ctx, middleware.Identity{UserID: uid, SessionID: sid, Access: middleware.UserAccess(uid)}), nil
}

// imageUpdateDenied is the permission check of an image update without a
// response: why the caller of r may not update the server container id,
// or "" if it may.
func (h *Handlers) imageUpdateDenied(r *http.Request, id string) string {
	if !middleware.Allowed(r.Context(), rbac.PermContainersUpgrade) {
		return "needs the containers.upgrade permission"
	}
	res := h.containerScopeReq(r.Context(), "local", id)
	if !middleware.AllowedOn(r.Context(), rbac.PermContainersUpgrade, res) {
		return "the container is outside your scope"
	}
	if h.Updater != nil {
		if stack := h.Updater.ManagedStack(r.Context(), id); stack != "" &&
			!middleware.AllowedOn(r.Context(), rbac.PermStacksDeploy, h.stackScopeReq(r.Context(), "local", stack)) {
			return "updating a container of stack " + stack + " needs the stacks.deploy permission"
		}
	}
	return ""
}
