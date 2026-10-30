package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/go-chi/chi/v5"
)

// GetGitSource returns the configured git source for a stack (or 404).
func (h *Handlers) GetGitSource(w http.ResponseWriter, r *http.Request) {
	if h.GitSource == nil {
		writeError(w, http.StatusServiceUnavailable, "git source service not configured")
		return
	}
	name := chi.URLParam(r, "name")
	src, err := h.GitSource.Get(r.Context(), name)
	if errors.Is(err, gitsource.ErrNotFound) {
		// Return 200 with null body instead of 404. The stack detail
		// page loads git-source on every render; a 404 makes the browser
		// console look broken for the common case where a stack simply
		// has no git source. The UI treats null and 404 identically.
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, src)
}

// ConfigureGitSource upserts the git source and does the first sync so
// the stack's FS is populated before the response returns. The sync
// failure is reported as a soft warning — the config is saved either
// way so the user can fix credentials and retry.
func (h *Handlers) ConfigureGitSource(w http.ResponseWriter, r *http.Request) {
	if h.GitSource == nil {
		writeError(w, http.StatusServiceUnavailable, "git source service not configured")
		return
	}
	name := chi.URLParam(r, "name")
	var in gitsource.Input
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	src, err := h.GitSource.Configure(r.Context(), name, in)
	if errors.Is(err, gitsource.ErrInvalidAuthKind) || errors.Is(err, gitsource.ErrPollTooShort) ||
		errors.Is(err, gitsource.ErrStackNameInvalid) || errors.Is(err, gitsource.ErrPathOutsideRepo) ||
		errors.Is(err, gitsource.ErrWebhookSecretShort) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// First sync fills the stack FS. Save the config either way (so the
	// user can fix creds and retry) but surface sync failure with 422 +
	// clear `sync_ok: false` so the UI can display it loudly instead of
	// silently pretending the connect succeeded.
	syncResult, syncErr := h.GitSource.Sync(r.Context(), name)
	h.audit(r, "stack.git_configure", name, map[string]any{"repo": in.RepoURL})
	h.syncGitWebhookNotice(r.Context())
	res := map[string]any{
		"source":  src,
		"sync_ok": syncErr == nil,
	}
	if syncErr != nil {
		res["sync_error"] = syncErr.Error()
		writeJSON(w, http.StatusUnprocessableEntity, res)
		return
	}
	res["sync"] = syncResult
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) DeleteGitSource(w http.ResponseWriter, r *http.Request) {
	if h.GitSource == nil {
		writeError(w, http.StatusServiceUnavailable, "git source service not configured")
		return
	}
	name := chi.URLParam(r, "name")
	if err := h.GitSource.Delete(r.Context(), name); err != nil {
		if errors.Is(err, gitsource.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "stack.git_disconnect", name, nil)
	h.syncGitWebhookNotice(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// SyncGitSource triggers a manual pull. If the source has auto_deploy,
// a successful sync with a new SHA will also deploy the stack.
func (h *Handlers) SyncGitSource(w http.ResponseWriter, r *http.Request) {
	if h.GitSource == nil {
		writeError(w, http.StatusServiceUnavailable, "git source service not configured")
		return
	}
	name := chi.URLParam(r, "name")
	res, err := h.GitSource.Sync(r.Context(), name)
	if errors.Is(err, gitsource.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no git source configured")
		return
	}
	if errors.Is(err, stacks.ErrModifiedOutside) {
		changed, _ := h.Stacks.ModifiedOutside(name)
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": stacks.ErrModifiedOutside.Error(),
			"code":  "modified_outside",
			"stack": name,
			"files": changed,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.audit(r, "stack.git_sync", name, map[string]any{"sha": res.NewSHA, "changed": res.Changed})
	writeJSON(w, http.StatusOK, res)
}

// GitWebhook is the PUBLIC endpoint GitHub / GitLab / Gitea POST to on
// push events. The stack's source must have a webhook_secret and the
// request must carry a matching signature:
//
//   - GitHub:  X-Hub-Signature-256: sha256=<hex>
//   - GitLab:  X-Gitlab-Token: <secret>   (plain shared secret)
//   - Gitea:   X-Gitea-Signature: <hex>   (sha256 HMAC, like GitHub)
//
// Without a secret the webhook is disabled: anyone who knows the stack
// name could otherwise trigger syncs and auto-deploys.
func (h *Handlers) GitWebhook(w http.ResponseWriter, r *http.Request) {
	if h.GitSource == nil {
		writeError(w, http.StatusServiceUnavailable, "git source service not configured")
		return
	}
	name := chi.URLParam(r, "name")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	// Unknown stack, missing secret and bad signature all get the same 401
	// so the public endpoint doesn't reveal which stacks exist.
	secret, err := h.GitSource.WebhookSecret(r.Context(), name)
	switch {
	case errors.Is(err, gitsource.ErrNotFound):
		writeError(w, http.StatusUnauthorized, "webhook rejected")
		return
	case err != nil:
		slog.Warn("git webhook secret lookup failed", "stack", name, "err", err)
		writeError(w, http.StatusInternalServerError, "webhook lookup failed")
		return
	case secret == "":
		slog.Info("git webhook rejected: no webhook secret set", "stack", name)
		writeError(w, http.StatusUnauthorized, "webhook rejected")
		return
	case !verifyWebhookSignature(r, body, secret):
		writeError(w, http.StatusUnauthorized, "webhook rejected")
		return
	}
	trigger := h.gitWebhookTrigger
	if trigger == nil {
		trigger = h.GitSource.TriggerSync
	}
	trigger(r.Context(), name)
	h.audit(r, "stack.git_webhook", name, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sync queued"})
}

// syncGitWebhookNotice re-evaluates the missing-webhook-secret notice after
// a git source changed. Failures only delay the notice until the next
// change or restart.
func (h *Handlers) syncGitWebhookNotice(ctx context.Context) {
	if h.GitSource == nil || h.Notices == nil {
		return
	}
	if err := h.GitSource.SyncWebhookNotice(ctx, h.Notices); err != nil {
		slog.Warn("git webhook notice update failed", "err", err)
	}
}

// verifyWebhookSignature checks the incoming signature against the
// shared secret, accepting either a GitHub/Gitea HMAC-SHA256 digest
// or a GitLab plain shared-secret token.
func verifyWebhookSignature(r *http.Request, body []byte, secret string) bool {
	// GitLab: plain secret on X-Gitlab-Token.
	if tok := r.Header.Get("X-Gitlab-Token"); tok != "" {
		return hmac.Equal([]byte(tok), []byte(secret))
	}
	// GitHub + Gitea: HMAC-SHA256 hex on X-Hub-Signature-256 or
	// X-Gitea-Signature. GitHub prefixes with "sha256=".
	got := r.Header.Get("X-Hub-Signature-256")
	if got == "" {
		got = r.Header.Get("X-Gitea-Signature")
	}
	got = strings.TrimPrefix(got, "sha256=")
	if got == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(got), []byte(want))
}
