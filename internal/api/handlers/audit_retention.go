package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
)

// GetAuditRetention returns the current retention config + a preview
// of what the next run would prune.
func (h *Handlers) GetAuditRetention(w http.ResponseWriter, r *http.Request) {
	if h.AuditRetention == nil {
		writeError(w, http.StatusServiceUnavailable, "audit retention service not configured")
		return
	}
	cfg := h.AuditRetention.Config()
	preview, err := h.AuditRetention.Preview(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"config":  cfg,
		"preview": preview,
	})
}

// UpdateAuditRetention validates + persists a new config through the
// settings store. Does NOT run the job — use /run for that.
func (h *Handlers) UpdateAuditRetention(w http.ResponseWriter, r *http.Request) {
	if h.AuditRetention == nil || h.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "audit retention service not configured")
		return
	}
	var cfg audit.RetentionConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := audit.SaveConfig(r.Context(), h.Settings.Set, cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "audit.retention_configure", cfg.Mode, map[string]any{
		"days":      cfg.Days,
		"target_id": cfg.TargetID,
	})
	h.GetAuditRetention(w, r)
}

// RunAuditRetention kicks off a one-shot retention pass. Admins can
// use this after changing the config to see the effect immediately
// without waiting for 03:00.
func (h *Handlers) RunAuditRetention(w http.ResponseWriter, r *http.Request) {
	if h.AuditRetention == nil {
		writeError(w, http.StatusServiceUnavailable, "audit retention service not configured")
		return
	}
	res, err := h.AuditRetention.Run(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.audit(r, "audit.retention_run", res.Mode, map[string]any{
		"pruned":   res.Pruned,
		"archived": res.Archived,
	})
	writeJSON(w, http.StatusOK, res)
}
