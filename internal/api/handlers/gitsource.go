package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/gitsource"
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
		writeError(w, http.StatusNotFound, "no git source configured")
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
	if errors.Is(err, gitsource.ErrInvalidAuthKind) || errors.Is(err, gitsource.ErrPollTooShort) || errors.Is(err, gitsource.ErrStackNameInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// First sync fills the stack FS. Failures here are reported as
	// a partial result — the config itself saved successfully.
	syncResult, syncErr := h.GitSource.Sync(r.Context(), name)
	h.audit(r, "stack.git_configure", name, map[string]any{"repo": in.RepoURL})
	res := map[string]any{
		"source": src,
	}
	if syncErr != nil {
		res["sync_error"] = syncErr.Error()
	} else {
		res["sync"] = syncResult
	}
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
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.audit(r, "stack.git_sync", name, map[string]any{"sha": res.NewSHA, "changed": res.Changed})
	writeJSON(w, http.StatusOK, res)
}

// GitWebhook is the PUBLIC endpoint GitHub / GitLab / Gitea POST to on
// push events. If the stack's source has a webhook_secret configured,
// we verify the HMAC before kicking off the sync. Signature formats:
//
//   - GitHub:  X-Hub-Signature-256: sha256=<hex>
//   - GitLab:  X-Gitlab-Token: <secret>   (plain shared secret)
//   - Gitea:   X-Gitea-Signature: <hex>   (sha256 HMAC, like GitHub)
//
// No auth is required if no secret is configured — the caller just
// has to know the stack name. That is weaker than signed webhooks but
// matches what Portainer ships as the default.
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
	secret, err := h.GitSource.WebhookSecret(r.Context(), name)
	if errors.Is(err, gitsource.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no git source configured")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if secret != "" {
		if !verifyWebhookSignature(r, body, secret) {
			writeError(w, http.StatusUnauthorized, "signature mismatch")
			return
		}
	}
	// Sync in the background so we don't block the webhook sender.
	// Webhooks are fire-and-forget — the provider retries on timeout.
	go func() {
		if _, err := h.GitSource.Sync(r.Context(), name); err != nil {
			// Already recorded on the row by the service; just log.
			_ = err
		}
	}()
	h.audit(r, "stack.git_webhook", name, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sync queued"})
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
