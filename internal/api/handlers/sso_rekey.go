package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/audit"
)

// unlinkedDetails records how many accounts a provider change detached.
func unlinkedDetails(n int64) map[string]string {
	if n == 0 {
		return nil
	}
	return map[string]string{"unlinked_accounts": strconv.FormatInt(n, 10)}
}

// GetSSORekey lists the OAuth2 and LDAP accounts still waiting for their
// move to an immutable subject.
func (h *Handlers) GetSSORekey(w http.ResponseWriter, r *http.Request) {
	p, err := h.Auth.PendingSSORekeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// EndSSORekey ends the re-key for every OAuth2 and LDAP account that is
// still waiting for it.
func (h *Handlers) EndSSORekey(w http.ResponseWriter, r *http.Request) {
	n, err := h.Auth.EndSSORekey(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionSSORekeyEnd, "", map[string]string{"accounts": strconv.FormatInt(n, 10)})
	h.syncSSORekeyNotices(r.Context())
	writeJSON(w, http.StatusOK, map[string]int64{"ended": n})
}

// auditSSORekey records a sign-in that moved an account to its immutable
// subject.
func (h *Handlers) auditSSORekey(r *http.Request, userID, kind, slug string) {
	if userID == "" {
		return
	}
	h.audit(r, audit.ActionSSORekey, userID, map[string]string{"provider": slug, "kind": kind})
	h.syncSSORekeyNotices(r.Context())
}

// syncSSORekeyNotices updates both pending re-key notices after accounts
// were re-keyed or unlinked. A failure only delays them until the next
// change or restart.
func (h *Handlers) syncSSORekeyNotices(ctx context.Context) {
	if h.Notices == nil {
		return
	}
	if h.Auth != nil {
		if err := h.Auth.SyncSSORekeyNotice(ctx, h.Notices); err != nil {
			slog.Warn("sso re-key notice update failed", "err", err)
		}
	}
	h.syncSAMLRekeyNotice(ctx)
}
