package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/go-chi/chi/v5"
)

// imageErrorStatus classifies docker-daemon image errors into the right
// HTTP status so the UI can show actionable messages. "Unknown" errors
// still return 500.
func imageErrorStatus(err error) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	if errors.Is(err, host.ErrAgentOffline) || errors.Is(err, host.ErrNoDocker) ||
		errors.Is(err, host.ErrAgentNoVolumeManage) || errors.Is(err, host.ErrAgentNoResourceManage) ||
		errors.Is(err, host.ErrAgentNoImageTransfer) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, scanner.ErrArchiveTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "conflict"),
		strings.Contains(msg, "is being used by"),
		strings.Contains(msg, "volume is in use"),
		strings.Contains(msg, "is in use"),
		strings.Contains(msg, "already exists"),
		strings.Contains(msg, "has active endpoints"):
		return http.StatusConflict
	case strings.Contains(msg, "pull access denied"),
		strings.Contains(msg, "repository does not exist"),
		strings.Contains(msg, "manifest unknown"),
		strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "no such image"),
		strings.Contains(msg, "invalid reference format"),
		strings.Contains(msg, "not found"):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

type pullRequest struct {
	Image string `json:"image"`
}

// imageRow is the all-mode row type for ListImages. Embeds the docker
// image.Summary so its fields flatten into the final JSON alongside
// host_id / host_name.
type imageRow struct {
	image.Summary
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
}

func (h *Handlers) ListImages(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	hostID := r.URL.Query().Get("host")

	// All-mode: fan out. Each online host contributes its image list,
	// tagged with host metadata per row.
	if host.IsAll(hostID) && h.Hosts != nil {
		targets := h.hostsInReadScope(r, h.Hosts.PickAll(r.Context()))
		res := host.FanOut(r.Context(), targets, func(ctx context.Context, hh host.Host) ([]imageRow, error) {
			list, err := hh.ListImages(ctx, all)
			if err != nil {
				return nil, err
			}
			rows := make([]imageRow, len(list))
			for i, im := range list {
				rows[i] = imageRow{
					Summary:  im,
					HostID:   hh.ID(),
					HostName: hh.Name(),
				}
			}
			return rows, nil
		})
		writeJSON(w, http.StatusOK, res)
		return
	}

	// Single-host path. Uses the host.Host interface instead of h.Docker
	// directly so a remote host picker actually shows remote images
	// (pre-P.6 this handler always returned local images regardless of
	// the selected host).
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.inReadScope(r, target.ID(), "") {
		writeError(w, http.StatusForbidden, "out of scope: your role does not include this host")
		return
	}
	images, err := target.ListImages(r.Context(), all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, images)
}

// maxPullLine bounds one progress line of a pull stream; Docker's are
// well under 1 KiB.
const maxPullLine = 1 << 20

// PullImage pulls on the host named by ?host= with the credentials that
// apply to that host, and relays the progress as ndjson.
func (h *Handlers) PullImage(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	scopeReq := h.hostScopeReq(target.ID())
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermImagesCreate, scopeReq, "host "+target.ID())
		return
	}
	var req pullRequest
	if err := decodeJSON(r, &req); err != nil || req.Image == "" {
		writeError(w, http.StatusBadRequest, "image required")
		return
	}
	var registryAuth string
	auditMeta := map[string]string{"host": target.ID()}
	if h.Registries != nil {
		auth, reg, err := h.Registries.ResolveForHost(r.Context(), req.Image, target.ID())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "resolve registry auth: "+err.Error())
			return
		}
		if reg != nil {
			registryAuth = auth
			auditMeta["registry"] = reg.Name
		}
	}
	secrets := operations.RegistryAuthSecrets(registryAuth)
	rc, err := target.PullImage(r.Context(), req.Image, registryAuth)
	if err != nil {
		writeError(w, imageErrorStatus(err), operations.MaskText(err.Error(), secrets))
		return
	}
	defer rc.Close()

	// An agent reports a pull it couldn't start as the stream's first read,
	// which can still become a proper status code.
	lines := bufio.NewScanner(rc)
	lines.Buffer(make([]byte, 0, 64<<10), maxPullLine)
	more := lines.Scan()
	if err := lines.Err(); !more && err != nil {
		writeError(w, imageErrorStatus(err), operations.MaskText(err.Error(), secrets))
		return
	}
	h.audit(r, audit.ActionImagePull, req.Image, auditMeta)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	writeLine := func(line []byte) {
		_, _ = w.Write(append(line, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}
	for ; more; more = lines.Scan() {
		writeLine([]byte(operations.MaskText(lines.Text(), secrets)))
	}
	// The status is long sent, so a stream that breaks off (agent gone)
	// has to look like Docker's own in-stream failure.
	if err := lines.Err(); err != nil {
		line, _ := json.Marshal(map[string]string{"error": operations.MaskText(err.Error(), secrets)})
		writeLine(line)
	}
}

// InspectImage returns the full Docker ImageInspect for the chosen
// host (?host=…, defaults local). The 501-on-remote fallback that
// used to live here is gone now that the agent protocol carries the
// inspect frame (FrameReqImageInspect, 2026-05-29).
func (h *Handlers) InspectImage(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	scopeReq := h.hostScopeReq(target.ID())
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermImagesView, scopeReq, "host "+target.ID())
		return
	}
	id, _ := url.PathUnescape(chi.URLParam(r, "id"))
	info, err := target.InspectImage(r.Context(), id)
	if err != nil {
		writeError(w, imageErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *Handlers) RemoveImage(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	scopeReq := h.hostScopeReq(target.ID())
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermImagesDelete, scopeReq, "host "+target.ID())
		return
	}
	id, _ := url.PathUnescape(chi.URLParam(r, "id"))
	force := r.URL.Query().Get("force") == "true"
	deleted, err := target.RemoveImage(r.Context(), id, force)
	if err != nil {
		writeError(w, imageErrorStatus(err), err.Error())
		return
	}
	h.audit(r, audit.ActionImageRemove, id, map[string]string{"host": target.ID()})
	writeJSON(w, http.StatusOK, deleted)
}

func (h *Handlers) PruneImages(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	scopeReq := h.hostScopeReq(target.ID())
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermImagesDelete, scopeReq, "host "+target.ID())
		return
	}
	report, err := target.PruneImages(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionImagePrune, "", map[string]any{
		"space_reclaimed": int64(report.SpaceReclaimed),
		"host":            target.ID(),
	})
	writeJSON(w, http.StatusOK, report)
}

// ListImageUpdates returns the cache from the background update watcher
// as a map keyed by image reference. UI uses this both for the "updates
// available" badge per container and for the resources/images-tab
// drawer that lists which images have newer manifests upstream.
//
//	GET /api/v1/images/updates
func (h *Handlers) ListImageUpdates(w http.ResponseWriter, r *http.Request) {
	// The watcher checks the server's own containers only.
	if h.UpdateWatcher == nil || !h.hostVisible(r, "local") {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, h.UpdateWatcher.Snapshot())
}
