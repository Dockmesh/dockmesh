package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup"
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
	var in backup.JobInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
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
