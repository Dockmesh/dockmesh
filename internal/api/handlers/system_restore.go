package handlers

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/restore"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/setup"
)

// stageMu keeps staging, applying and discarding from running at once.
var stageMu sync.Mutex

// GetStagedRestore reports a system restore waiting for the next start.
//
//	GET /restore/system
func (h *Handlers) GetStagedRestore(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsAdmin(r) {
		writeError(w, http.StatusForbidden, "only an admin can restore the whole system")
		return
	}
	if h.RestoreConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "system restore not configured")
		return
	}
	info, err := restore.Staged(h.RestoreConfig)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	leftovers, err := restore.Leftovers(h.RestoreConfig)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"staged": info, "leftovers": leftovers})
}

// StageSystemRestore prepares a system restore from a saved run
// (JSON {"run_id": N}) or an uploaded archive (multipart "file", plus
// "key" for an archive encrypted by another install). The archive is
// extracted next to the live files and checked; nothing live changes
// until POST /restore/system/apply restarts the server.
//
//	POST /restore/system
func (h *Handlers) StageSystemRestore(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsAdmin(r) {
		writeError(w, http.StatusForbidden, "only an admin can restore the whole system")
		return
	}
	if h.RestoreConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "system restore not configured")
		return
	}
	if !stageMu.TryLock() {
		writeError(w, http.StatusConflict, "another restore is being staged")
		return
	}
	defer stageMu.Unlock()
	var (
		src    io.Reader
		source string
		opts   restore.StageOptions
	)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		rc, name, ok := h.openUploadedSystemBackup(w, r)
		if !ok {
			return
		}
		defer rc.Close()
		src, source = rc, "upload:"+name
	} else {
		var req struct {
			RunID int64 `json:"run_id"`
		}
		if err := decodeJSON(r, &req); err != nil || req.RunID <= 0 {
			writeError(w, http.StatusBadRequest, "run_id or a multipart upload is required")
			return
		}
		if h.Backups == nil {
			writeError(w, http.StatusServiceUnavailable, "backups not configured")
			return
		}
		run, err := h.Backups.GetRun(r.Context(), req.RunID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if !h.requireRunAccess(w, r, run, rbac.PermBackupsRestore) {
			return
		}
		if len(run.Sources) == 0 || run.Sources[0].Type != "system" {
			writeError(w, http.StatusBadRequest, "run is not a system backup")
			return
		}
		rc, hashCheck, err := h.Backups.OpenRunHashed(r.Context(), req.RunID)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		defer rc.Close()
		src, source = rc, "run:"+strconv.FormatInt(req.RunID, 10)
		// Checked before the restore is marked ready, so a tampered
		// archive is never applied, not even by an unplanned restart.
		opts.Verify = func() error {
			if c := hashCheck(); c.Status == "fail" {
				return fmt.Errorf("%w: %s", errHashMismatch, c.Message)
			}
			return nil
		}
	}
	counts, result, err := restore.Stage(r.Context(), src, h.RestoreConfig, source, opts)
	if err != nil {
		writeStageError(w, err, result)
		return
	}
	info, _ := restore.Staged(h.RestoreConfig)
	h.audit(r, "system.restore_stage", source, map[string]any{"files": counts.Files, "bytes": counts.Bytes})
	writeJSON(w, http.StatusOK, map[string]any{"staged": info, "counts": counts, "sanity": result})
}

var errHashMismatch = errors.New("archive hash mismatch")

// ApplySystemRestore restarts the server so the staged restore is
// swapped in before anything opens the database.
//
//	POST /restore/system/apply
func (h *Handlers) ApplySystemRestore(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsAdmin(r) {
		writeError(w, http.StatusForbidden, "only an admin can restore the whole system")
		return
	}
	if h.RestoreConfig == nil || h.RequestRestart == nil {
		writeError(w, http.StatusServiceUnavailable, "system restore not configured")
		return
	}
	if !stageMu.TryLock() {
		writeError(w, http.StatusConflict, "a restore is being staged")
		return
	}
	defer stageMu.Unlock()
	info, err := restore.Staged(h.RestoreConfig)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info == nil {
		writeError(w, http.StatusConflict, restore.ErrNothingStaged.Error())
		return
	}
	h.audit(r, "system.restore_apply", info.Source, nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "staged": info})
	h.RequestRestart()
}

// DiscardSystemRestore drops a staged restore.
//
//	DELETE /restore/system
func (h *Handlers) DiscardSystemRestore(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsAdmin(r) {
		writeError(w, http.StatusForbidden, "only an admin can restore the whole system")
		return
	}
	if h.RestoreConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "system restore not configured")
		return
	}
	if !stageMu.TryLock() {
		writeError(w, http.StatusConflict, "a restore is being staged")
		return
	}
	defer stageMu.Unlock()
	if err := restore.DiscardStaged(h.RestoreConfig); errors.Is(err, restore.ErrNothingStaged) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.audit(r, "system.restore_discard", "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// DeleteRestoreLeftovers deletes the copies earlier system restores kept
// of the files they replaced.
//
//	DELETE /restore/system/leftovers
func (h *Handlers) DeleteRestoreLeftovers(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsAdmin(r) {
		writeError(w, http.StatusForbidden, "only an admin can delete what a system restore kept")
		return
	}
	if h.RestoreConfig == nil {
		writeError(w, http.StatusServiceUnavailable, "system restore not configured")
		return
	}
	if !stageMu.TryLock() {
		writeError(w, http.StatusConflict, "a restore is being staged")
		return
	}
	defer stageMu.Unlock()
	n, err := restore.RemoveLeftovers(h.RestoreConfig)
	if n > 0 {
		h.audit(r, "system.restore_cleanup", "", map[string]any{"removed": n})
	}
	if h.Notices != nil {
		if serr := restore.SyncLeftoverNotice(r.Context(), h.RestoreConfig, h.Notices); serr != nil {
			slog.Warn("restore leftover notice", "err", serr)
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetupRestore is the setup wizard's "restore from backup" path: on a
// fresh install it stages an uploaded system backup (with the exported
// key file if it is encrypted), ends setup mode and restarts into the
// restored install.
//
//	POST /setup/restore
func (h *Handlers) SetupRestore(w http.ResponseWriter, r *http.Request) {
	if !h.setupWritable(w, r) {
		return
	}
	if h.RestoreConfig == nil || h.RequestRestart == nil {
		writeError(w, http.StatusServiceUnavailable, "system restore not configured")
		return
	}
	// The install slot keeps this and the wizard's own install apart, and
	// the upload may take long: the setup has to still be open, without
	// any account, once the archive is in.
	if !h.SetupState.TryBeginCommit(middleware.SetupToken(r)) {
		writeError(w, http.StatusConflict, "an install is already running, or setup has finished or expired")
		return
	}
	done := false
	defer func() {
		if !done {
			h.SetupState.EndCommit(false)
		}
	}()
	if !h.setupStillFree(w, r) {
		done = true
		return
	}
	if !stageMu.TryLock() {
		writeError(w, http.StatusConflict, "another restore is being staged")
		return
	}
	defer stageMu.Unlock()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(setupUploadTime))
	rc, name, ok := h.openUploadedSystemBackup(w, r)
	if !ok {
		return
	}
	defer rc.Close()
	counts, result, err := restore.Stage(r.Context(), rc, h.RestoreConfig, "setup:"+name, restore.StageOptions{})
	if err != nil {
		writeStageError(w, err, result)
		return
	}
	if !h.setupStillFree(w, r) {
		h.discardSetupRestore()
		done = true
		return
	}
	if h.FinishSetup != nil {
		if err := h.FinishSetup(); err != nil {
			h.discardSetupRestore()
			writeError(w, http.StatusInternalServerError, "end setup mode: "+err.Error())
			return
		}
	}
	h.SetupState.EndCommit(true)
	done = true
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "counts": counts, "sanity": result})
	h.RequestRestart()
}

// discardSetupRestore drops a staged setup restore that was refused; if
// that fails it would be applied at the next start, so it is logged loudly.
func (h *Handlers) discardSetupRestore() {
	if err := restore.DiscardStaged(h.RestoreConfig); err != nil {
		slog.Error("discard the refused setup restore failed; remove the staging directory before the next restart", "err", err)
	}
}

// setupUploadTime bounds how long the setup restore may take to receive
// its archive.
const setupUploadTime = 30 * time.Minute

// setupStillFree answers 409 and ends setup mode once the install has any
// account, since the wizard would write over it. It releases the install
// slot whenever it returns false.
func (h *Handlers) setupStillFree(w http.ResponseWriter, r *http.Request) bool {
	if h.DB == nil {
		return true
	}
	exists, err := setup.UsersExist(r.Context(), h.DB)
	if err != nil {
		h.SetupState.EndCommit(false)
		writeError(w, http.StatusInternalServerError, "could not check for existing accounts")
		return false
	}
	if exists {
		h.SetupState.EndCommit(true)
		writeError(w, http.StatusConflict, "this Dockmesh is already set up, sign in instead")
		return false
	}
	return true
}

func writeStageError(w http.ResponseWriter, err error, result *restore.SanityResult) {
	switch {
	case errors.Is(err, restore.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, errHashMismatch):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, restore.ErrEncryptedBackup):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case result != nil:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "sanity": result})
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

// openUploadedSystemBackup reads the multipart "file" (and optional
// "key") and returns the archive as plaintext. Encrypted archives are
// decrypted with the uploaded key file and, when this install has
// secrets, its own keys.
func (h *Handlers) openUploadedSystemBackup(w http.ResponseWriter, r *http.Request) (io.ReadCloser, string, bool) {
	file, header, ok := parseBackupUpload(w, r)
	if !ok {
		return nil, "", false
	}
	var ids []age.Identity
	if kf, _, err := r.FormFile("key"); err == nil {
		b, rerr := io.ReadAll(io.LimitReader(kf, 1<<20))
		kf.Close()
		if rerr != nil {
			file.Close()
			writeError(w, http.StatusBadRequest, "read key file: "+rerr.Error())
			return nil, "", false
		}
		parsed, perr := secrets.ParseKeyFile(b)
		if perr != nil {
			file.Close()
			writeError(w, http.StatusBadRequest, "key file: "+perr.Error())
			return nil, "", false
		}
		ids = append(ids, parsed...)
	}
	if h.Secrets != nil && h.Secrets.Enabled() {
		if own, err := h.Secrets.Identities(); err == nil {
			ids = append(ids, own...)
		}
	}
	plain, err := restore.Plaintext(file, ids)
	switch {
	case errors.Is(err, restore.ErrEncryptedBackup):
		file.Close()
		writeError(w, http.StatusUnprocessableEntity, "the backup is encrypted; add the backup key file exported from the original install")
		return nil, "", false
	case err != nil:
		file.Close()
		writeError(w, http.StatusUnprocessableEntity, "decrypt backup: "+err.Error()+" (wrong key file?)")
		return nil, "", false
	}
	return readCloser{Reader: plain, Closer: file}, header.Filename, true
}

type readCloser struct {
	io.Reader
	io.Closer
}
