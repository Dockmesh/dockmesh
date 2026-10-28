package handlers

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

func (h *Handlers) ListBackupJobs(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	jobs, err := h.Backups.ListJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (h *Handlers) GetBackupJob(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	job, err := h.Backups.GetJob(r.Context(), id)
	if errors.Is(err, backup.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handlers) CreateBackupJob(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	var in backup.JobInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !h.checkBackupJobScope(w, r, in) {
		return
	}
	job, err := h.Backups.CreateJob(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionStackCreate, "backup:"+job.Name, nil)
	writeJSON(w, http.StatusCreated, job)
}

func (h *Handlers) UpdateBackupJob(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !h.requireStoredJobAccess(w, r, id) {
		return
	}
	var in backup.JobInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !h.checkBackupJobScope(w, r, in) {
		return
	}
	job, err := h.Backups.UpdateJob(r.Context(), id, in)
	if errors.Is(err, backup.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionStackUpdate, "backup:"+job.Name, nil)
	writeJSON(w, http.StatusOK, job)
}

func (h *Handlers) DeleteBackupJob(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !h.requireStoredJobAccess(w, r, id) {
		return
	}
	if err := h.Backups.DeleteJob(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDelete, "backup:"+strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// AcknowledgeBackupJobReview clears the needs_review flag on a backup
// job. Mode is taken from the URL: "keep" preserves the job, "disable"
// also flips enabled=0 so the scheduler stops firing it.
func (h *Handlers) AcknowledgeBackupJobReview(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !h.requireStoredJobAccess(w, r, id) {
		return
	}
	mode := chi.URLParam(r, "mode")
	if mode != "keep" && mode != "disable" {
		writeError(w, http.StatusBadRequest, "mode must be 'keep' or 'disable'")
		return
	}
	if err := h.Backups.AcknowledgeReview(r.Context(), id, mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackUpdate, "backup:"+strconv.FormatInt(id, 10), map[string]string{"review": mode})
	w.WriteHeader(http.StatusNoContent)
}

// AcceptBackupHostKey trusts the changed SFTP host key a job last saw.
//
//	POST /api/v1/backups/jobs/{id}/accept-host-key
func (h *Handlers) AcceptBackupHostKey(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !h.requireStoredJobAccess(w, r, id) {
		return
	}
	err = h.Backups.AcceptHostKey(r.Context(), id)
	switch {
	case errors.Is(err, backup.ErrJobNotFound):
		writeError(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, backup.ErrNoPendingHostKey):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "backup.accept_host_key", "backup-job:"+strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) RunBackupJob(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !h.requireStoredJobAccess(w, r, id) {
		return
	}
	run, err := h.Backups.RunNow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDeploy, "backup:run:"+strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, run)
}

func (h *Handlers) ListBackupRuns(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	runs, err := h.Backups.ListRuns(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// GetBackupRun returns a single run row. The run-detail drawer in the
// UI polls this while a manual run is in flight; the list endpoint
// would force the frontend to filter client-side and miss status
// transitions for runs that aged out of the limit window.
func (h *Handlers) GetBackupRun(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	run, err := h.Backups.GetRun(r.Context(), id)
	if errors.Is(err, backup.ErrRunNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// GetBackupRunLog returns the captured log lines for one run, in
// chronological order. Includes info/warn/error narration emitted by
// the executor at phase boundaries plus verbatim hook stdout/stderr
// (capped at 16 KB per hook so noisy hooks can't blow the table).
//
//	GET /api/v1/backups/runs/{id}/log
func (h *Handlers) GetBackupRunLog(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	entries, err := h.Backups.RunLog(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// DownloadBackupArchive streams the run's archive bytes to the
// caller. The Service handles transparent decryption for runs that
// were stored encrypted (via age) — admins downloading an archive
// always get plaintext tar.gz, never the on-target ciphertext.
func (h *Handlers) DownloadBackupArchive(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	run, err := h.Backups.GetRun(r.Context(), id)
	if errors.Is(err, backup.ErrRunNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if run.Status != "success" {
		writeError(w, http.StatusConflict, "can only download from a successful run")
		return
	}
	src, err := h.Backups.ReadRun(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer src.Close()
	filename := "backup-run-" + strconv.FormatInt(id, 10) + ".tar.gz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if _, err := io.Copy(w, src); err != nil {
		// At this point headers are flushed — no way to surface a
		// JSON error to the client.
		return
	}
	h.audit(r, audit.ActionStackUpdate, "backup:download:"+strconv.FormatInt(id, 10), nil)
}

type restoreRequest struct {
	DestVolume string `json:"dest_volume"`
}

func (h *Handlers) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req restoreRequest
	if err := decodeJSON(r, &req); err != nil || req.DestVolume == "" {
		writeError(w, http.StatusBadRequest, "dest_volume required")
		return
	}
	if err := h.Backups.CheckRestoreTarget(r.Context(), id, req.DestVolume); err != nil {
		if errors.Is(err, docker.ErrInvalidVolumeName) {
			writeError(w, http.StatusBadRequest, "dest_volume must be a Docker volume name, not a path")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Backups.Restore(r.Context(), id, req.DestVolume); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionContainerRollback, "backup:restore:"+strconv.FormatInt(id, 10), map[string]string{"dest": req.DestVolume})
	w.WriteHeader(http.StatusNoContent)
}

// RestoreStackBackup is the explicit stack-restore entry point. Takes
// an optional target stack name (defaults to the source name in the
// run) and writes the archive's stack/* files into /stacks/<name>/
// plus untars any volumes/<vol>.tar.gz into matching docker volumes.
// Returns a structured report so the UI can show what happened.
func (h *Handlers) RestoreStackBackup(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		TargetStack string `json:"target_stack,omitempty"`
	}
	_ = decodeJSON(r, &req) // body is optional — empty means "same name as source"
	report, err := h.Backups.RestoreStack(r.Context(), id, req.TargetStack)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionContainerRollback, "backup:restore-stack:"+strconv.FormatInt(id, 10), map[string]string{"target": report.StackName})
	writeJSON(w, http.StatusOK, report)
}

// checkBackupJobScope requires the job's host to be in the caller's scope.
// Hooks run commands inside containers, so they also need containers.exec
// and every hook container must be in the caller's host and role scope.
func (h *Handlers) checkBackupJobScope(w http.ResponseWriter, r *http.Request, in backup.JobInput) bool {
	hostID := in.HostID
	if hostID == "" {
		hostID = "local"
	}
	if !h.requireHostAccess(w, r, hostID) {
		return false
	}
	hooks := append(append([]backup.Hook{}, in.PreHooks...), in.PostHooks...)
	if len(hooks) == 0 {
		return true
	}
	if !roleAllows(r, rbac.PermContainersExec) {
		writeError(w, http.StatusForbidden, "backup hooks run commands in containers and need the containers.exec permission")
		return false
	}
	for _, hk := range hooks {
		if hk.Container == "" {
			writeError(w, http.StatusBadRequest, "hook container required")
			return false
		}
		// Inspect on the job's host: the hook runs there, and a container with
		// the same name elsewhere says nothing about it.
		req, ok := h.hookScopeReq(r, hostID, hk.Container)
		if !ok {
			writeError(w, http.StatusBadRequest, "hook container "+hk.Container+" not found on the job's host")
			return false
		}
		if !h.checkRoleScope(r, req) {
			h.writeRoleScopeDenied(w, r, rbac.PermContainersExec, req, "container "+hk.Container)
			return false
		}
	}
	return true
}

func (h *Handlers) hookScopeReq(r *http.Request, hostID, container string) (rbac.ScopeRequest, bool) {
	if h.Hosts == nil {
		return rbac.ScopeRequest{}, false
	}
	target, err := h.Hosts.Pick(hostID)
	if err != nil {
		return rbac.ScopeRequest{}, false
	}
	info, err := target.InspectContainer(r.Context(), container)
	if err != nil {
		return rbac.ScopeRequest{}, false
	}
	req := h.hostScopeReq(r.Context(), hostID)
	if info.Config != nil {
		req.StackName = info.Config.Labels["com.docker.compose.project"]
	}
	return req, true
}

// requireStoredJobAccess checks the stored job's host against the caller's
// scope before anything acts on it.
func (h *Handlers) requireStoredJobAccess(w http.ResponseWriter, r *http.Request, id int64) bool {
	job, err := h.Backups.GetJob(r.Context(), id)
	if errors.Is(err, backup.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "not found")
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
	return h.requireHostAccess(w, r, hostID)
}
