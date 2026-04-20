package handlers

import (
	"net/http"
)

// ExportBackupKey returns the age private key used to encrypt stack .env
// files and the system-backup archive. Downloaded once per install, stored
// out of band, and needed for DR because the key lives INSIDE the
// encrypted backup otherwise (FINDING-37).
//
//	GET /api/v1/system/backup-key/export
//
// Admin-only (wired at the router layer). Every call is audited so a
// stolen key leaves a trail.
func (h *Handlers) ExportBackupKey(w http.ResponseWriter, r *http.Request) {
	if h.Secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets service not configured")
		return
	}
	body, err := h.Secrets.ExportKeyFile()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "backup.key_export", "system", nil)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="dockmesh-backup-key.txt"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}
