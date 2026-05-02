package handlers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

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
		writeError(w, http.StatusBadRequest, "extract: "+sanitizeExtractErr(err, dir))
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

// VerifyBackupRun reads a saved backup run from its target and runs
// the type-appropriate verifier. P.13.4: previously fell through to
// the system-only sanity flow regardless of source type, which gave
// confusing 422s for stack/volume runs ("db could not be opened" on
// archives that have no DB by design). Now:
//
//   - system runs: stream-extract to temp + run sanity (existing flow);
//     the temp dir is deleted as soon as sanity is done.
//   - stack runs:  walk the outer tar, validate every volumes/<v>.tar.gz
//     decompresses cleanly, confirm stack/compose.yaml is present, hash
//     the plaintext stream against run.SHA256.
//   - volume runs: gunzip-walk the archive; sha256 match.
//
// Admin-only.
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

	// Decide the type once so we can route system runs to the legacy
	// extract+sanity flow and stack/volume runs to the new structural
	// verify. The Service.VerifyRun helper handles the latter; system
	// runs still need internal/restore for the deep DB checks.
	runType, err := h.Backups.RunSourceType(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if runType == "system" {
		h.verifySystemRun(w, r, id)
		return
	}
	res, err := h.Backups.VerifyRun(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "backup.verify_run", strconv.FormatInt(id, 10), map[string]any{
		"type":   res.Type,
		"bytes":  res.Counts.Bytes,
		"passed": res.Passed,
	})
	status := http.StatusOK
	if !res.Passed {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{
		"run_id": id,
		"type":   res.Type,
		"counts": res.Counts,
		"sanity": map[string]any{
			"passed":  res.Passed,
			"checks":  res.Checks,
			"summary": res.Summary,
		},
	})
}

// verifySystemRun keeps the existing system-backup verify flow:
// stream the run through ExtractToTemp + Sanity, return the result.
// Pulled into its own helper so the type-dispatch in VerifyBackupRun
// stays readable.
func (h *Handlers) verifySystemRun(w http.ResponseWriter, r *http.Request, id int64) {
	src, err := h.Backups.ReadRun(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	defer src.Close()

	dir, cfg, counts, err := restore.ExtractToTemp(r.Context(), src, "run")
	if err != nil {
		if errors.Is(err, restore.ErrEncryptedBackup) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "extract: "+sanitizeExtractErr(err, dir))
		return
	}
	defer os.RemoveAll(dir)

	result, err := restore.Sanity(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "backup.verify_run", strconv.FormatInt(id, 10), map[string]any{
		"type":   "system",
		"files":  counts.Files,
		"bytes":  counts.Bytes,
		"passed": result.Passed,
	})
	status := http.StatusOK
	if !result.Passed {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{
		"run_id": id,
		"type":   "system",
		"counts": counts,
		"sanity": result,
	})
}

// sanitizeExtractErr strips the temp-dir prefix from error messages so
// external API consumers don't see server filesystem layout. The
// original error is still available in logs.
func sanitizeExtractErr(err error, tmpDir string) string {
	msg := err.Error()
	if tmpDir != "" {
		msg = strings.ReplaceAll(msg, tmpDir, "<tmp>")
	}
	// Also strip common OS-temp prefixes in case the error didn't use the
	// final tmpDir but a sub-path.
	for _, p := range []string{"/tmp/dockmesh-verify-"} {
		if i := strings.Index(msg, p); i >= 0 {
			// replace up to next whitespace
			end := strings.IndexAny(msg[i:], " \"\t\n")
			if end == -1 {
				msg = msg[:i] + "<tmp>"
			} else {
				msg = msg[:i] + "<tmp>" + msg[i+end:]
			}
		}
	}
	return msg
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
