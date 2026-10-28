package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// callerIsAdmin reports whether the caller may change system settings and
// is limited to no host or stack. Material that unlocks the whole install
// (system archives, the backup key) is restricted to such callers.
func (h *Handlers) callerIsAdmin(r *http.Request) bool {
	return middleware.Allowed(r.Context(), rbac.PermSystemUpdate) && h.callerUnscoped(r)
}

// requireArchiveAccess decides whether the caller may download run's
// archive: system archives need an admin, all others the job's host in the
// caller's scope. A run whose job is gone can only be fetched by an admin.
func (h *Handlers) requireArchiveAccess(w http.ResponseWriter, r *http.Request, run *backup.Run) bool {
	for _, src := range run.Sources {
		if src.Type == "system" {
			if h.callerIsAdmin(r) {
				return true
			}
			writeError(w, http.StatusForbidden, "only an admin can download system backups")
			return false
		}
	}
	job, err := h.Backups.GetJob(r.Context(), run.JobID)
	if errors.Is(err, backup.ErrJobNotFound) {
		if h.callerIsAdmin(r) {
			return true
		}
		writeError(w, http.StatusForbidden, "only an admin can download runs of a deleted job")
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	hostID := job.HostID
	if hostID == "" {
		hostID = "local"
	}
	if !h.requireHostAccess(w, r, hostID) {
		return false
	}
	scopeReq := h.hostScopeReq(r.Context(), hostID)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermBackupsDownload, scopeReq, "")
		return false
	}
	return true
}
