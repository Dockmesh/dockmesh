package handlers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/restore"
	"github.com/go-chi/chi/v5"
)

// VerifyUploadedBackup accepts a multipart-uploaded system-backup
// tarball, extracts it to a scratch directory, runs the post-restore
// sanity checks against the extracted files, and discards the scratch
// dir. Never touches the live install. Admin-only. P.12.4.
//
// Use case: "before I run dockmesh restore against my prod server
// for real, verify this tarball is even valid". Answers the
// enterprise-evaluator question "are our backups actually restorable?"
// without needing a spare host.
func (h *Handlers) VerifyUploadedBackup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 30); err != nil { // 1 GiB in-memory cap, spills to disk
		writeError(w, http.StatusBadRequest, "parse multipart: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing 'file' part in form")
		return
	}
	defer file.Close()

	dir, cfg, counts, err := restore.ExtractToTemp(r.Context(), file, "upload")
	if err != nil {
		if errors.Is(err, restore.ErrEncryptedBackup) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "extract: "+err.Error())
		return
	}
	defer os.RemoveAll(dir)

	result, err := restore.Sanity(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "backup.verify_upload", header.Filename, map[string]any{
		"files":  counts.Files,
		"bytes":  counts.Bytes,
		"passed": result.Passed,
	})

	status := http.StatusOK
	if !result.Passed {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{
		"filename": header.Filename,
		"counts":   counts,
		"sanity":   result,
	})
}

// VerifyBackupRun reads a saved backup run from its target, streams
// it through the same extract-to-temp + sanity flow, and reports.
// Admin-only. P.12.4 (rolled in from former P.12.20 scope).
//
// This only applies to **system** backup runs — volume / stack runs
// don't have the file-level structure we check. A future slice can
// extend this to spin up ephemeral stacks on a test host for stack
// runs (the original P.12.20 idea).
func (h *Handlers) VerifyBackupRun(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backup service not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	// Reading the archive requires the backup service to expose the
	// same download path restore uses internally. For MVP we route
	// through the existing Restore RunID lookup — but we need a
	// read-only accessor. Since backup.Service.Restore() writes to a
	// destination volume, we can't reuse it directly. Instead we ask
	// the service for the encrypted+raw stream via a new helper on
	// the service; that's not yet implemented, so surface a clear
	// error pointing at the upload-based verify for now.
	//
	// TODO: backup.Service.ReadRun(ctx, runID) → io.ReadCloser.
	// Until then, operators use the upload endpoint after downloading
	// the tarball manually from their backup target.
	_ = id
	writeError(w, http.StatusNotImplemented,
		"verify-by-run-id requires a ReadRun helper on the backup service — not yet shipped. "+
			"Workaround: download the archive from your backup target and POST it to /api/v1/restore/verify instead.")
}

// uploadLimit is the upper bound we accept for an incoming backup
// tarball. 1 GiB covers realistic Dockmesh installs (DB + stacks +
// data combined is usually tens of MB). Larger uploads indicate
// either a misconfigured backup or an attempted DoS.
const uploadLimit = 1 << 30

// readBytes is a helper — kept in case we need to buffer a tarball
// before streaming it through multiple passes (not used today; the
// verify flow is single-pass through ExtractToTemp).
func readBytes(r io.Reader, cap int) ([]byte, error) {
	var buf bytes.Buffer
	lr := io.LimitReader(r, int64(cap))
	_, err := io.Copy(&buf, lr)
	return buf.Bytes(), err
}
