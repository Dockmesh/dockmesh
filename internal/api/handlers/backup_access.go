package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// callerIsAdmin reports whether the caller may change instance settings,
// which only an assignment on everything grants. Material that unlocks the
// whole install (system archives) is restricted to such callers.
func (h *Handlers) callerIsAdmin(r *http.Request) bool {
	return middleware.AccessOf(r.Context()).AllowsGlobally(rbac.PermSystemUpdate)
}

// requireJobAccess guards every write to a backup job. A
// system source puts the whole install (database, secrets, backup key)
// into the archive and wherever its target points, so such jobs are
// admin-only; any other job needs its host, before and after the change,
// in the caller's scope. prev is nil on create, next is nil when the job
// itself is not being rewritten.
func (h *Handlers) requireJobAccess(w http.ResponseWriter, r *http.Request, prev *backup.Job, next *backup.JobInput) bool {
	system := (prev != nil && backup.HasSystemSource(prev.Sources)) || (next != nil && backup.HasSystemSource(next.Sources))
	if system {
		if h.callerIsAdmin(r) {
			return true
		}
		writeError(w, http.StatusForbidden, "only an admin can manage or run system backup jobs")
		return false
	}
	var hosts []string
	if prev != nil {
		hosts = append(hosts, prev.HostID)
	}
	if next != nil {
		hosts = append(hosts, next.HostID)
	}
	for _, hostID := range hosts {
		if hostID == "" {
			hostID = "local"
		}
		if !h.requireHostAccess(w, r, hostID) {
			return false
		}
		scopeReq := h.hostScopeReq(r.Context(), hostID)
		if !h.checkRoleScope(r, scopeReq) {
			h.writeRoleScopeDenied(w, r, rbac.PermBackupsUpdate, scopeReq, "")
			return false
		}
	}
	return true
}

// requireTargetAccess guards writes to a saved target (its settings, its
// accepted host key): they change where every job using it writes, so the
// caller needs requireJobAccess for each of those jobs, deleted ones
// included. A target with a system job is admin-only.
func (h *Handlers) requireTargetAccess(w http.ResponseWriter, r *http.Request, targetID int64) bool {
	if h.Backups == nil {
		return true
	}
	jobs, err := h.Backups.JobsUsingTarget(r.Context(), targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	for i := range jobs {
		if !h.requireJobAccess(w, r, &jobs[i], nil) {
			return false
		}
	}
	return true
}

// requireRunAccess guards everything that reads or restores a run's
// archive or log: system runs need an admin, all others the job's host in
// the caller's scope. A run whose job is gone is admin-only.
func (h *Handlers) requireRunAccess(w http.ResponseWriter, r *http.Request, run *backup.Run, perm rbac.Perm) bool {
	if backup.HasSystemSource(run.Sources) {
		if h.callerIsAdmin(r) {
			return true
		}
		writeError(w, http.StatusForbidden, "only an admin can access system backups")
		return false
	}
	job, err := h.Backups.GetJob(r.Context(), run.JobID)
	if errors.Is(err, backup.ErrJobNotFound) {
		if h.callerIsAdmin(r) {
			return true
		}
		writeError(w, http.StatusForbidden, "only an admin can access runs of a deleted job")
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
		h.writeRoleScopeDenied(w, r, perm, scopeReq, "")
		return false
	}
	return true
}

// backupRunVisible applies backupJobVisible to the job a run belongs to.
// System runs and runs of a deleted job are admin-only, like their
// archives. jobs holds the current jobs by id.
func (h *Handlers) backupRunVisible(r *http.Request, run *backup.Run, jobs map[int64]backup.Job) bool {
	if backup.HasSystemSource(run.Sources) {
		return h.callerIsAdmin(r)
	}
	job, ok := jobs[run.JobID]
	if !ok {
		return h.callerIsAdmin(r)
	}
	return h.backupJobVisible(r, job)
}

func (h *Handlers) backupJobsByID(r *http.Request) (map[int64]backup.Job, error) {
	jobs, err := h.Backups.ListJobs(r.Context())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]backup.Job, len(jobs))
	for _, j := range jobs {
		out[j.ID] = j
	}
	return out, nil
}

// backupTargetVisible hides a saved target whose jobs the caller cannot
// all see: its settings name the share, bucket or server those jobs write
// to. A target no job uses stays visible so it can be picked for a new job.
func (h *Handlers) backupTargetVisible(r *http.Request, targetID int64) (bool, error) {
	if h.Backups == nil || h.callerIsAdmin(r) {
		return true, nil
	}
	jobs, err := h.Backups.JobsUsingTarget(r.Context(), targetID)
	if err != nil {
		return false, err
	}
	for _, j := range jobs {
		if !h.backupJobVisible(r, j) {
			return false, nil
		}
	}
	return true, nil
}

// runForAccess loads the run named by {id} and applies requireRunAccess.
func (h *Handlers) runForAccess(w http.ResponseWriter, r *http.Request, perm rbac.Perm) (*backup.Run, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return nil, false
	}
	run, err := h.Backups.GetRun(r.Context(), id)
	if errors.Is(err, backup.ErrRunNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !h.requireRunAccess(w, r, run, perm) {
		return nil, false
	}
	return run, true
}

// jobForAccess loads the job named by {id} and applies requireJobAccess.
func (h *Handlers) jobForAccess(w http.ResponseWriter, r *http.Request) (*backup.Job, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return nil, false
	}
	job, err := h.Backups.GetJob(r.Context(), id)
	if errors.Is(err, backup.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !h.requireJobAccess(w, r, job, nil) {
		return nil, false
	}
	return job, true
}
