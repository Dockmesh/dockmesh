package handlers

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	dtypes "github.com/docker/docker/api/types"
	"github.com/go-chi/chi/v5"
)

// imageErrorStatus classifies docker-daemon image errors into the right
// HTTP status so the UI can show actionable messages. "Unknown" errors
// still return 500.
func imageErrorStatus(err error) int {
	if err == nil {
		return http.StatusInternalServerError
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
// ImageSummary so its fields flatten into the final JSON alongside
// host_id / host_name.
type imageRow struct {
	dtypes.ImageSummary
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
}

func (h *Handlers) ListImages(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	hostID := r.URL.Query().Get("host")

	// All-mode: fan out. Each online host contributes its image list,
	// tagged with host metadata per row.
	if host.IsAll(hostID) && h.Hosts != nil {
		targets := h.Hosts.PickAll(r.Context())
		res := host.FanOut(r.Context(), targets, func(ctx context.Context, hh host.Host) ([]imageRow, error) {
			list, err := hh.ListImages(ctx, all)
			if err != nil {
				return nil, err
			}
			rows := make([]imageRow, len(list))
			for i, im := range list {
				rows[i] = imageRow{
					ImageSummary: im,
					HostID:       hh.ID(),
					HostName:     hh.Name(),
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
	images, err := target.ListImages(r.Context(), all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, images)
}

func (h *Handlers) PullImage(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	var req pullRequest
	if err := decodeJSON(r, &req); err != nil || req.Image == "" {
		writeError(w, http.StatusBadRequest, "image required")
		return
	}
	// Registry credentials lookup (P.11.7). Local-pull only — the host
	// picker is not consulted because the PullImage endpoint itself runs
	// against h.Docker, not a fanout. The scope check is still there:
	// we pass empty hostTags so only "all hosts" registry entries match,
	// which is the correct behaviour for pulls on the central server's
	// own docker daemon.
	var registryAuth string
	var usedRegistryName string
	if h.Registries != nil {
		auth, reg, err := h.Registries.ResolveAuth(r.Context(), req.Image, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "resolve registry auth: "+err.Error())
			return
		}
		if reg != nil {
			registryAuth = auth
			usedRegistryName = reg.Name
		}
	}
	rc, err := h.Docker.PullImageWithAuth(r.Context(), req.Image, registryAuth)
	if err != nil {
		writeError(w, imageErrorStatus(err), err.Error())
		return
	}
	defer rc.Close()
	auditMeta := map[string]string{}
	if usedRegistryName != "" {
		auditMeta["registry"] = usedRegistryName
	}
	if len(auditMeta) > 0 {
		h.audit(r, audit.ActionImagePull, req.Image, auditMeta)
	} else {
		h.audit(r, audit.ActionImagePull, req.Image, nil)
	}
	// Stream the pull progress to the client as ndjson.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		buf := make([]byte, 4096)
		for {
			n, err := rc.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				f.Flush()
			}
			if err != nil {
				break
			}
		}
	} else {
		_, _ = io.Copy(w, rc)
	}
}

func (h *Handlers) RemoveImage(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
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
