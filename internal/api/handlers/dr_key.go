package handlers

import (
	"fmt"
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

// RotateSecretsKey generates a new age key, re-encrypts every stack
// `.env.age` against it, atomically swaps the in-memory key for the
// live server, and archives the old key to .../secrets.age-key.old.
// Equivalent to the `dockmesh secrets rotate` CLI — but runs in the
// same process so no service restart is needed (FINDING-52: running
// server kept using old key in RAM while CLI had already written the
// new one to disk → every stack read 500'd with "decrypt: identity did
// not match any of the recipients").
//
//	POST /api/v1/system/secrets/rotate
//
// Admin-only (enforced at router level).
func (h *Handlers) RotateSecretsKey(w http.ResponseWriter, r *http.Request) {
	if h.Secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets service not configured")
		return
	}
	if h.Stacks == nil {
		writeError(w, http.StatusServiceUnavailable, "stacks manager not configured")
		return
	}
	if !h.Secrets.Enabled() {
		writeError(w, http.StatusBadRequest, "secrets encryption is disabled on this server")
		return
	}
	// Snapshot the current key before mutating it so the re-encryption
	// walk below can still decrypt legacy .env.age files.
	oldSnap := h.Secrets.Snapshot()
	newSvc, err := h.Secrets.RotateInMemory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("generate new key: %v", err))
		return
	}
	count, err := h.Stacks.ReencryptAllBetween(oldSnap, newSvc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("reencrypt stacks: %v", err))
		return
	}
	oldRecipient, err := h.Secrets.AdoptAndPersist(newSvc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("persist new key: %v", err))
		return
	}
	h.audit(r, "secrets.rotate", "system", map[string]any{
		"reencrypted": count,
		"old_public":  oldRecipient,
		"new_public":  h.Secrets.PublicRecipient(),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"reencrypted":   count,
		"old_recipient": oldRecipient,
		"new_recipient": h.Secrets.PublicRecipient(),
	})
}
