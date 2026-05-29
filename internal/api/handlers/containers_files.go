package handlers

import (
	"encoding/base64"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// BrowseContainerFiles lists one directory level inside the running
// container's filesystem. Tar-stream walked at depth 1 — works on
// scratch / distroless images because no shell is involved.
//
//	GET /api/v1/containers/{id}/files?path=/etc
func (h *Handlers) BrowseContainerFiles(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.containerScopeReq(r.Context(), target.ID(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersView, scopeReq, "container "+id)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/"
	}
	entries, err := target.ContainerBrowseEntries(r.Context(), id, p)
	if err != nil {
		writeError(w, mapBrowseStatus(err), err.Error())
		return
	}
	h.audit(r, audit.ActionContainerBrowse, id, map[string]string{"path": p, "host": target.ID()})
	if entries == nil {
		entries = []host.VolumeEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// ReadContainerFile returns the first slice of a file inside the
// container as JSON {content, size, truncated, binary}. Used by the
// preview pane in the UI. For large or binary files use the
// /content/download endpoint instead.
//
//	GET /api/v1/containers/{id}/files/content?path=/etc/hostname
func (h *Handlers) ReadContainerFile(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.containerScopeReq(r.Context(), target.ID(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersView, scopeReq, "container "+id)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	res, err := target.ContainerReadFile(r.Context(), id, p, 0)
	if err != nil {
		writeError(w, mapBrowseStatus(err), err.Error())
		return
	}
	h.audit(r, audit.ActionContainerReadFile, id, map[string]string{"path": p, "host": target.ID()})
	writeJSON(w, http.StatusOK, res)
}

// DownloadContainerFile streams a file's raw bytes out of the container
// with a Content-Disposition: attachment header so the browser triggers
// a download. Used for binary files or files larger than the preview cap.
//
//	GET /api/v1/containers/{id}/files/download?path=/var/log/app.log
func (h *Handlers) DownloadContainerFile(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.containerScopeReq(r.Context(), target.ID(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersView, scopeReq, "container "+id)
		return
	}
	p := r.URL.Query().Get("path")
	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	rc, name, size, err := target.ContainerDownloadFile(r.Context(), id, p)
	if err != nil {
		writeError(w, mapBrowseStatus(err), err.Error())
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+"\"")
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	h.audit(r, audit.ActionContainerReadFile, id, map[string]string{"path": p, "host": target.ID(), "mode": "download"})
	_, _ = io.Copy(w, rc)
}

// containerFileWriteRequest is the body for the upload endpoint. The
// content is base64-encoded so binary uploads work over plain JSON.
// Mode is optional — defaults to 0644.
type containerFileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"` // base64-encoded
	Mode    int64  `json:"mode,omitempty"`
}

// WriteContainerFile uploads a single file into the container at the
// caller-supplied absolute path. Strict permission: containers.exec,
// because writing a file into a running container is effectively code
// injection (e.g. /usr/local/bin/init, /etc/cron.d/*, /var/spool/cron).
//
//	POST /api/v1/containers/{id}/files
//	{ "path": "/etc/foo.conf", "content": "<base64>", "mode": 420 }
func (h *Handlers) WriteContainerFile(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	id := chi.URLParam(r, "id")
	scopeReq := h.containerScopeReq(r.Context(), target.ID(), id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersExec, scopeReq, "container "+id)
		return
	}
	var req containerFileWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	data, decErr := base64.StdEncoding.DecodeString(req.Content)
	if decErr != nil {
		writeError(w, http.StatusBadRequest, "content must be base64-encoded")
		return
	}
	if err := target.ContainerWriteFile(r.Context(), id, req.Path, data, req.Mode); err != nil {
		writeError(w, mapBrowseStatus(err), err.Error())
		return
	}
	h.audit(r, audit.ActionContainerWriteFile, id, map[string]string{
		"path":  req.Path,
		"host":  target.ID(),
		"bytes": strconv.Itoa(len(data)),
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": req.Path, "bytes": len(data)})
}

// sanitizeFilename strips path separators + control chars so a hostile
// filename like "../../etc/passwd" or "foo\r\nX-Set-Cookie: bad" can't
// poison the Content-Disposition header.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\"", "_")
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "download"
	}
	return out
}

