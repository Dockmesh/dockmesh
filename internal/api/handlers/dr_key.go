package handlers

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/dockmesh/dockmesh/internal/audit"
)

// ExportBackupKey returns the age private key used to encrypt stack .env
// files and the system-backup archive. Downloaded once per install, stored
// out of band, and needed for DR because the key lives INSIDE the
// encrypted backup otherwise (FINDING-37).
//
//	GET /api/v1/system/backup-key/export
//
// Admin-only. Every call is audited so a stolen key leaves a trail.
func (h *Handlers) ExportBackupKey(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsAdmin(r) {
		writeError(w, http.StatusForbidden, "only an admin can export the backup key")
		return
	}
	if h.Secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets service not configured")
		return
	}
	body, err := h.Secrets.ExportKeyFile()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionBackupKeyExport, "system", nil)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="dockmesh-backup-key.txt"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

var rotateMu sync.Mutex

// RotateSecretsKey generates a new age key, records old and new key in
// the key history, re-encrypts every stack `.env.age` against the new key,
// then atomically replaces the key file and the in-memory key.
// Equivalent to the `dockmesh secrets rotate` CLI, but runs in-process so
// no restart is needed.
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
	// Two overlapping rotations would each write a history without the
	// other's new key.
	if !rotateMu.TryLock() {
		writeError(w, http.StatusConflict, "a key rotation is already running")
		return
	}
	defer rotateMu.Unlock()
	// Snapshot the current key before mutating it so the re-encryption
	// walk below can still decrypt legacy .env.age files.
	oldSnap := h.Secrets.Snapshot()
	newSvc, err := h.Secrets.RotateInMemory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("generate new key: %v", err))
		return
	}
	if err := h.Secrets.PrepareRotation(newSvc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
