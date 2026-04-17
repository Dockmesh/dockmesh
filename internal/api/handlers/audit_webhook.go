package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
)

// GetAuditWebhook returns the current webhook config (url + filter +
// has_secret flag; the plaintext secret is never returned).
func (h *Handlers) GetAuditWebhook(w http.ResponseWriter, r *http.Request) {
	if h.AuditWebhook == nil {
		writeError(w, http.StatusServiceUnavailable, "audit webhook not configured")
		return
	}
	writeJSON(w, http.StatusOK, h.AuditWebhook.Config())
}

// UpdateAuditWebhook persists a new URL / secret / filter. Empty
// secret with clear_secret=false keeps the stored value.
func (h *Handlers) UpdateAuditWebhook(w http.ResponseWriter, r *http.Request) {
	if h.AuditWebhook == nil {
		writeError(w, http.StatusServiceUnavailable, "audit webhook not configured")
		return
	}
	var in audit.WebhookInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	cfg, err := h.AuditWebhook.SaveConfig(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "audit.webhook_configure", cfg.URL, map[string]any{
		"has_secret":     cfg.HasSecret,
		"filter_actions": cfg.FilterActions,
	})
	writeJSON(w, http.StatusOK, cfg)
}

// TestAuditWebhook POSTs a synthetic entry so operators can verify
// the receiver wiring before enabling the live feed.
func (h *Handlers) TestAuditWebhook(w http.ResponseWriter, r *http.Request) {
	if h.AuditWebhook == nil {
		writeError(w, http.StatusServiceUnavailable, "audit webhook not configured")
		return
	}
	if err := h.AuditWebhook.SendTest(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.audit(r, "audit.webhook_test", "", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "test sent"})
}
