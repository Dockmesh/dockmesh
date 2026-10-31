package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/go-chi/chi/v5"
)

func (h *Handlers) ListBackupTargets(w http.ResponseWriter, r *http.Request) {
	if h.BackupTargets == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	list, err := h.BackupTargets.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]*targets.StoredTarget, len(list))
	for i := range list {
		out[i] = redactTarget(&list[i])
	}
	writeJSON(w, http.StatusOK, out)
}

// BackupLocalPath tells the backup wizards which directory to prefill for
// a local target.
func (h *Handlers) BackupLocalPath(w http.ResponseWriter, r *http.Request) {
	path := ""
	if h.Backups != nil {
		path = h.Backups.DefaultLocalPath()
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

func (h *Handlers) CreateBackupTarget(w http.ResponseWriter, r *http.Request) {
	if h.BackupTargets == nil {
		writeError(w, http.StatusServiceUnavailable, "backup targets unavailable")
		return
	}
	var in targets.TargetInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Type == "local" {
		if err := targets.CheckLocalConfig(in.Config); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	t, err := h.BackupTargets.Create(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, redactTarget(t))
}

func (h *Handlers) UpdateBackupTarget(w http.ResponseWriter, r *http.Request) {
	if h.BackupTargets == nil {
		writeError(w, http.StatusServiceUnavailable, "backup targets unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in targets.TargetInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if prev, err := h.BackupTargets.Get(r.Context(), id); err == nil && prev.Type == in.Type {
		if in.Config, err = keepConfig(in.Config, prev.Config, backupTargetSecrets); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if in.Type == "local" {
		if err := targets.CheckLocalConfig(in.Config); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	t, err := h.BackupTargets.Update(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, redactTarget(t))
}

func (h *Handlers) DeleteBackupTarget(w http.ResponseWriter, r *http.Request) {
	if h.BackupTargets == nil {
		writeError(w, http.StatusServiceUnavailable, "backup targets unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.BackupTargets.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestBackupTarget verifies credentials and returns storage info.
func (h *Handlers) TestBackupTarget(w http.ResponseWriter, r *http.Request) {
	if h.BackupTargets == nil {
		writeError(w, http.StatusServiceUnavailable, "backup targets unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	stored, err := h.BackupTargets.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "target not found")
		return
	}

	// Try to build and query storage
	type storageQuerier interface {
		StorageInfo() (int64, int64, error)
	}

	tgt, buildErr := h.BackupTargets.BuildChecked(r.Context(), stored, h.Notices)
	if buildErr != nil {
		_ = h.BackupTargets.UpdateStatus(r.Context(), id, "error", 0, 0)
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": buildErr.Error()})
		return
	}

	// Try storage info
	var total, used int64
	if sq, ok := tgt.(storageQuerier); ok {
		total, used, err = sq.StorageInfo()
		if err != nil {
			_ = h.BackupTargets.UpdateStatus(r.Context(), id, "error", 0, 0)
			writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
			return
		}
	}

	// Real reachability probe. For local + sftp the target's own Open
	// path is idempotent ({create parent dirs}), so List on an empty
	// dir succeeds. For S3 + WebDAV, List hits the remote service and
	// a bad endpoint / bad creds surfaces here — we MUST report that
	// as error instead of silently claiming "connected".
	probeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, listErr := tgt.List(probeCtx, ""); listErr != nil {
		// Local "dir doesn't exist" is created by NewLocal itself, so a
		// List failure at this point for any target really means the
		// remote is unhappy.
		_ = h.BackupTargets.UpdateStatus(r.Context(), id, "error", 0, 0)
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "error",
			"error":  listErr.Error(),
		})
		return
	}

	_ = h.BackupTargets.UpdateStatus(r.Context(), id, "connected", total, used)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "connected",
		"total_bytes": total,
		"used_bytes":  used,
		"free_bytes":  total - used,
	})
}

// AcceptBackupTargetHostKey trusts the changed SFTP host key a stored
// target last saw.
//
//	POST /api/v1/backups/targets/{id}/accept-host-key
func (h *Handlers) AcceptBackupTargetHostKey(w http.ResponseWriter, r *http.Request) {
	if h.BackupTargets == nil {
		writeError(w, http.StatusServiceUnavailable, "backup targets unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := h.BackupTargets.Get(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "target not found")
		return
	}
	err = h.BackupTargets.AcceptHostKey(r.Context(), id, h.Notices)
	switch {
	case errors.Is(err, targets.ErrNoPendingHostKey):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "backup.accept_target_host_key", "backup-target:"+strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// TestBackupTargetConfig tests a raw config without saving (for the dialog).
//
//	POST /api/v1/backups/targets/test-config
func (h *Handlers) TestBackupTargetConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		targets.TargetInput
		TargetID int64 `json:"target_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// An unsaved config has nowhere to record a key; when it edits a
	// stored SFTP target, the server must still present the stored key.
	cfg := targets.KeepHostKey("", nil, in.Type, in.Config)
	if in.TargetID > 0 && h.BackupTargets != nil {
		if stored, err := h.BackupTargets.Get(r.Context(), in.TargetID); err == nil {
			cfg = targets.KeepHostKey(stored.Type, stored.Config, in.Type, in.Config)
		}
	}
	tgt, err := buildTargetFromInput(in.Type, cfg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	type storageQuerier interface {
		StorageInfo() (int64, int64, error)
	}
	var total, used int64
	if sq, ok := tgt.(storageQuerier); ok {
		total, used, err = sq.StorageInfo()
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"status": "error", "error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "connected", "total_bytes": total, "used_bytes": used, "free_bytes": total - used,
	})
}

// DiscoverSMBShares lists available shares on an SMB server.
//
//	POST /api/v1/backups/targets/discover-shares
func (h *Handlers) DiscoverSMBShares(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	shares, err := targets.ListSMBShares(req.Host, req.Port, req.Username, req.Password)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"error": err.Error(), "shares": []string{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": shares})
}

func buildTargetFromInput(typ string, cfg any) (targets.Target, error) {
	switch typ {
	case "local":
		return targets.NewLocal(cfg)
	case "s3":
		return targets.NewS3(cfg)
	case "sftp":
		return targets.NewSFTP(cfg)
	case "smb":
		return targets.NewSMB(cfg)
	case "webdav":
		return targets.NewWebDAV(cfg)
	}
	return nil, fmt.Errorf("unknown target type: %s", typ)
}
