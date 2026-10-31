package handlers

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/restore"
)

// VerifyUploadedBackup accepts a multipart-uploaded system-backup
// tarball, extracts it to a scratch directory, runs the post-restore
// sanity checks against the extracted files, and discards the scratch
// dir. Never touches the live install. Admin-only: the report describes
// the install's database and secrets.
//
// Use case: "before I run dockmesh restore against my prod server
// for real, verify this tarball is even valid". Answers the
// enterprise-evaluator question "are our backups actually restorable?"
// without needing a spare host.
func (h *Handlers) VerifyUploadedBackup(w http.ResponseWriter, r *http.Request) {
	if !h.callerIsAdmin(r) {
		writeError(w, http.StatusForbidden, "only an admin can verify system backups")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit+(1<<20))
	// Up to 32 MiB in memory, the rest spills to a temp file.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "backup larger than 1 GiB")
			return
		}
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
		if errors.Is(err, restore.ErrTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
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
// the type-appropriate verifier:
//
//   - system runs: stream-extract to temp + run sanity;
//     the temp dir is deleted as soon as sanity is done.
//   - stack runs:  walk the outer tar, validate every volumes/<v>.tar.gz
//     decompresses cleanly, confirm stack/compose.yaml is present, check
//     the stored bytes against run.SHA256.
//   - volume runs: gunzip-walk the archive; sha256 match.
//
// System runs are admin-only; other runs need the job's host in scope.
func (h *Handlers) VerifyBackupRun(w http.ResponseWriter, r *http.Request) {
	if h.Backups == nil {
		writeError(w, http.StatusServiceUnavailable, "backup service not configured")
		return
	}
	run, ok := h.runForAccess(w, r, rbac.PermBackupsRestore)
	if !ok {
		return
	}
	id := run.ID
	// System runs need internal/restore for the deep DB checks.
	if backup.HasSystemSource(run.Sources) {
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

// verifySystemRun streams a system run through ExtractToTemp + Sanity.
func (h *Handlers) verifySystemRun(w http.ResponseWriter, r *http.Request, id int64) {
	src, hashCheck, err := h.Backups.OpenRunHashed(r.Context(), id)
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
	sum := hashCheck()
	result.Checks = append(result.Checks, restore.SanityCheck{Name: sum.Name, Status: sum.Status, Message: sum.Message})
	if sum.Status == "fail" {
		result.Passed = false
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
