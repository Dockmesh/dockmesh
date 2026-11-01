package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/redact"
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
	out := make([]*backup.Job, len(jobs))
	for i := range jobs {
		out[i] = redactJob(&jobs[i])
	}
	writeJSON(w, http.StatusOK, out)
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
	writeJSON(w, http.StatusOK, redactJob(job))
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
	if err := resolveJobTargetSecrets(&in, nil); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.requireJobAccess(w, r, nil, &in) || !h.checkBackupJobScope(w, r, in) {
		return
	}
	job, err := h.Backups.CreateJob(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.syncBackupNameNotice(r)
	h.audit(r, audit.ActionStackCreate, "backup:"+job.Name, nil)
	writeJSON(w, http.StatusCreated, redactJob(job))
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
	prev, err := h.Backups.GetJob(r.Context(), id)
	if errors.Is(err, backup.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !h.requireJobAccess(w, r, prev, &in) || !h.checkBackupJobScope(w, r, in) {
		return
	}
	if err := resolveJobTargetSecrets(&in, prev); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	h.syncBackupNameNotice(r)
	h.audit(r, audit.ActionStackUpdate, "backup:"+job.Name, nil)
	writeJSON(w, http.StatusOK, redactJob(job))
}

func (h *Handlers) DeleteBackupJob(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	job, ok := h.jobForAccess(w, r)
	if !ok {
		return
	}
	if err := h.Backups.DeleteJob(r.Context(), job.ID); errors.Is(err, backup.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.syncBackupNameNotice(r)
	h.audit(r, audit.ActionStackDelete, "backup:"+strconv.FormatInt(job.ID, 10), nil)
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
	mode := chi.URLParam(r, "mode")
	if mode != "keep" && mode != "disable" {
		writeError(w, http.StatusBadRequest, "mode must be 'keep' or 'disable'")
		return
	}
	job, ok := h.jobForAccess(w, r)
	if !ok {
		return
	}
	if err := h.Backups.AcknowledgeReview(r.Context(), job.ID, mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackUpdate, "backup:"+strconv.FormatInt(job.ID, 10), map[string]string{"review": mode})
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
	job, ok := h.jobForAccess(w, r)
	if !ok {
		return
	}
	if job.TargetID > 0 && !h.requireTargetAccess(w, r, job.TargetID) {
		return
	}
	id := job.ID
	err := h.Backups.AcceptHostKey(r.Context(), id)
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
	job, ok := h.jobForAccess(w, r)
	if !ok {
		return
	}
	run, err := h.Backups.StartRun(operations.WithActor(r.Context(), middleware.UserID(r.Context())), job.ID)
	switch {
	case errors.Is(err, backup.ErrJobNotFound):
		writeError(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, backup.ErrJobRunning):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDeploy, "backup:run:"+strconv.FormatInt(job.ID, 10), nil)
	writeJSON(w, http.StatusAccepted, run)
}

// GetBackupSchedule reports the time zone backup schedules run in.
func (h *Handlers) GetBackupSchedule(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	writeJSON(w, http.StatusOK, h.Backups.ScheduleInfo())
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
// Hook output can carry secrets, so the log is guarded like the archive.
//
//	GET /api/v1/backups/runs/{id}/log
func (h *Handlers) GetBackupRunLog(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	run, ok := h.runForAccess(w, r, rbac.PermBackupsView)
	if !ok {
		return
	}
	entries, err := h.Backups.RunLog(r.Context(), run.ID)
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
	run, ok := h.runForAccess(w, r, rbac.PermBackupsDownload)
	if !ok {
		return
	}
	id := run.ID
	if !run.Restorable() {
		writeError(w, http.StatusConflict, backup.ErrNotRestorable.Error())
		return
	}
	src, err := h.Backups.ReadRun(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer src.Close()
	h.audit(r, audit.ActionBackupDownload, "backup-run:"+strconv.FormatInt(id, 10), map[string]any{
		"job_id": run.JobID, "job_name": run.JobName,
	})
	filename := "backup-run-" + strconv.FormatInt(id, 10) + ".tar.gz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	// Headers are flushed once copying starts; a failure can't become
	// a JSON error anymore.
	_, _ = io.Copy(w, src)
}

type restoreRequest struct {
	DestVolume  string `json:"dest_volume"`
	TargetStack string `json:"target_stack"`
	HostID      string `json:"host_id"`
	Mode        string `json:"mode"`
}

// RestoreBackup restores a volume run into dest_volume (default: the
// backed-up volume) on host_id (default: the host it was taken on).
func (h *Handlers) RestoreBackup(w http.ResponseWriter, r *http.Request) {
	h.restoreRun(w, r, false)
}

// RestoreStackBackup restores a stack run: files into the stack directory
// (target_stack, default: the source name), volumes onto the host.
func (h *Handlers) RestoreStackBackup(w http.ResponseWriter, r *http.Request) {
	h.restoreRun(w, r, true)
}

func (h *Handlers) restoreRun(w http.ResponseWriter, r *http.Request, stackOnly bool) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backups not configured")
		return
	}
	run, ok := h.runForAccess(w, r, rbac.PermBackupsRestore)
	if !ok {
		return
	}
	id := run.ID
	var req restoreRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	if req.Mode != "" && req.Mode != "merge" && req.Mode != "replace" {
		writeError(w, http.StatusBadRequest, "mode must be merge or replace")
		return
	}
	if backup.HasSystemSource(run.Sources) {
		writeError(w, http.StatusConflict, backup.ErrSystemRestore.Error())
		return
	}
	kind := ""
	if len(run.Sources) > 0 {
		kind = run.Sources[0].Type
	}
	if stackOnly && kind != "stack" {
		writeError(w, http.StatusBadRequest, "run is not a stack backup")
		return
	}
	opts := backup.RestoreOptions{Target: req.DestVolume, HostID: req.HostID, Replace: req.Mode == "replace"}
	if kind == "stack" {
		opts.Target = req.TargetStack
		if opts.Target == "" {
			opts.Target = req.DestVolume
		}
	}
	origin, err := h.Backups.RunHost(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if opts.HostID == "" {
		opts.HostID = origin
	}
	// runForAccess covered the origin host; writing changes the
	// destination host, which must be in scope too.
	if !h.requireHostAccess(w, r, opts.HostID) {
		return
	}
	if scopeReq := h.hostScopeReq(r.Context(), opts.HostID); !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermBackupsRestore, scopeReq, "")
		return
	}
	if kind == "stack" {
		if opts.Target == "" {
			opts.Target = run.Sources[0].Name
		}
		if !h.requireStackRestoreTarget(w, r, opts.Target, opts.HostID) {
			return
		}
	}

	target := opts.Target
	if target == "" && len(run.Sources) > 0 {
		target = run.Sources[0].Name
	}
	op := h.Operations.Start(r.Context(), operations.Spec{
		Kind:   opBackupRestore,
		Target: target,
		Ref:    "backup-run:" + strconv.FormatInt(id, 10),
		HostID: opts.HostID,
		Actor:  middleware.UserID(r.Context()),
		Steps:  backup.RestoreSteps,
	})
	if kind == "stack" {
		op.AddSecrets(h.stackEnvValues(opts.Target)...)
	}
	// A restore stopped halfway leaves a half-written volume, so it runs to
	// the end even if the client disconnects.
	report, err := h.Backups.Restore(operations.WithOp(context.WithoutCancel(r.Context()), op), id, opts)
	op.Finish(err)
	var inUse *backup.VolumeInUseError
	switch {
	case errors.Is(err, docker.ErrInvalidVolumeName):
		writeError(w, http.StatusBadRequest, "dest_volume must be a Docker volume name, not a path")
		return
	case errors.Is(err, backup.ErrNotRestorable), errors.Is(err, backup.ErrSystemRestore), errors.Is(err, backup.ErrRestoreBusy),
		errors.Is(err, backup.ErrUnverifiedReplace), errors.Is(err, host.ErrAgentTooOld), errors.As(err, &inUse):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, backup.ErrArchiveInvalid), errors.Is(err, backup.ErrArchiveMismatch):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionContainerRollback, "backup:restore:"+strconv.FormatInt(id, 10), map[string]string{
		"kind": report.Kind, "target": opts.Target, "host": report.HostID, "mode": report.Mode,
	})
	writeJSON(w, http.StatusOK, report)
}

// requireStackRestoreTarget guards writing a stack's files from a backup:
// it replaces compose.yaml and .env like PUT /stacks/{name} (or creates
// the stack like POST /stacks), so it needs that permission and the
// stack's scope, on the restore host and on the host the stack is
// deployed to.
func (h *Handlers) requireStackRestoreTarget(w http.ResponseWriter, r *http.Request, stack, hostID string) bool {
	perm := rbac.PermStacksCreate
	if h.Stacks != nil {
		if _, err := h.Stacks.Get(stack); err == nil {
			perm = rbac.PermStacksUpdate
		}
	}
	if !roleAllows(r, perm) {
		writeError(w, http.StatusForbidden, "restoring stack "+stack+" needs the "+string(perm)+" permission")
		return false
	}
	if scopeReq := h.stackScopeReq(r.Context(), "", stack); !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, perm, scopeReq, "stack "+stack)
		return false
	}
	if !h.requireStackScope(w, r, perm, hostID, stack) {
		return false
	}
	if h.Deployments != nil {
		if dep, err := h.Deployments.Get(r.Context(), stack); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		} else if dep != nil && dep.HostID != hostID && !h.requireStackScope(w, r, perm, dep.HostID, stack) {
			return false
		}
	}
	return true
}

// syncBackupNameNotice re-evaluates the shared-archive-folder notice after
// the set of jobs changed.
func (h *Handlers) syncBackupNameNotice(r *http.Request) {
	if err := h.Backups.SyncNameCollisionNotice(r.Context(), h.Notices); err != nil {
		slog.Warn("backup job name notice", "err", err)
	}
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

// resolveJobTargetSecrets fills the redacted secrets of an inline target
// config from the job's previous inline config. A job that references a
// saved target (TargetID) has no inline config: the target holds it.
func resolveJobTargetSecrets(in *backup.JobInput, prev *backup.Job) error {
	if in.TargetID > 0 {
		return nil
	}
	blank := blankedSecrets(in.TargetConfig)
	var err error
	if prev != nil && prev.TargetID == 0 && prev.TargetType == in.TargetType {
		if in.TargetConfig, err = keepConfig(in.TargetConfig, prev.TargetConfig, backupTargetSecrets); err != nil {
			return err
		}
	}
	m := redact.ToMap(in.TargetConfig)
	for _, k := range blank {
		if s, _ := m[k].(string); s == "" {
			return fmt.Errorf("the saved %s was not included; pick the target again or re-enter it", k)
		}
	}
	return nil
}

// blankedSecrets lists the secret fields a client sent back redacted
// ("" plus "<field>_set": true).
func blankedSecrets(cfg any) []string {
	m := redact.ToMap(cfg)
	var out []string
	for _, k := range backupTargetSecrets {
		if set, _ := m[k+redact.SetSuffix].(bool); set {
			if s, _ := m[k].(string); s == "" {
				out = append(out, k)
			}
		}
	}
	return out
}

// stackEnvValues returns the .env values of a stack, masked in the output
// of operations that touch it.
func (h *Handlers) stackEnvValues(name string) []string {
	if h.Stacks == nil || name == "" {
		return nil
	}
	d, err := h.Stacks.Get(name)
	if err != nil || d == nil {
		return nil
	}
	return operations.EnvValues(d.Env)
}
