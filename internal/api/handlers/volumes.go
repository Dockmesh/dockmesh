package handlers

import (
	"context"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/go-chi/chi/v5"
)

type volumeRequest struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Labels map[string]string `json:"labels,omitempty"`
}

// volumeRow is the all-mode row type for ListVolumes. The underlying
// volume type is `any` per the Host interface contract (Docker's volume
// list returns heterogeneous shapes), so we use json.RawMessage-like
// passthrough via interface{} and let the encoder flatten naturally
// via the Volume field as a named key. Unlike containers/images/networks
// we cannot use struct embedding here because `any` has no fields to
// embed; the frontend accesses volume data via `item.volume.*` in
// all-mode responses.
type volumeRow struct {
	Volume   any    `json:"volume"`
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
}

func (h *Handlers) ListVolumes(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")

	if host.IsAll(hostID) && h.Hosts != nil {
		targets := h.Hosts.PickAll(r.Context())
		res := host.FanOut(r.Context(), targets, func(ctx context.Context, hh host.Host) ([]volumeRow, error) {
			list, err := hh.ListVolumes(ctx)
			if err != nil {
				return nil, err
			}
			rows := make([]volumeRow, len(list))
			for i, v := range list {
				rows[i] = volumeRow{
					Volume:   v,
					HostID:   hh.ID(),
					HostName: hh.Name(),
				}
			}
			return rows, nil
		})
		writeJSON(w, http.StatusOK, res)
		return
	}

	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	vols, err := target.ListVolumes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vols)
}

func (h *Handlers) InspectVolume(w http.ResponseWriter, r *http.Request) {
	// Inspect is single-host. Frontend passes ?host=<id> when navigating
	// from all-mode so we route to the correct daemon.
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if target.ID() == "local" {
		if h.Docker == nil {
			writeError(w, http.StatusServiceUnavailable, "docker unavailable")
			return
		}
		vol, err := h.Docker.InspectVolume(r.Context(), chi.URLParam(r, "name"))
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, vol)
		return
	}
	// TODO(p.7): extend host.Host with InspectVolume for remote hosts.
	writeError(w, http.StatusNotImplemented, "volume inspect on remote hosts is planned for P.7")
}

func (h *Handlers) CreateVolume(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	var req volumeRequest
	if err := decodeJSON(r, &req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	vol, err := h.Docker.CreateVolume(r.Context(), req.Name, req.Driver, req.Labels)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionVolumeCreate, req.Name, nil)
	writeJSON(w, http.StatusCreated, vol)
}

func (h *Handlers) RemoveVolume(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	force := r.URL.Query().Get("force") == "true"
	if err := h.Docker.RemoveVolume(r.Context(), name, force); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionVolumeRemove, name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) PruneVolumes(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	report, err := h.Docker.PruneVolumes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionVolumePrune, "", map[string]uint64{"space_reclaimed": report.SpaceReclaimed})
	writeJSON(w, http.StatusOK, report)
}
