package handlers

import (
	"context"
	"encoding/json"
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

// volumeRow flattens the docker volume fields alongside host metadata
// so the frontend sees Name, Driver, Scope, host_id, host_name at
// the same level. We convert the `any` volume to a map and inject
// the host fields — this avoids the nesting problem that struct
// embedding can't solve for `any`.
type volumeRow = map[string]any

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
				row := toMap(v)
				row["host_id"] = hh.ID()
				row["host_name"] = hh.Name()
				rows[i] = row
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
	vol, err := target.InspectVolume(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vol)
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

// toMap converts an arbitrary struct to a map[string]any via JSON
// round-trip. Used to flatten volume data alongside host metadata.
func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	m := make(map[string]any)
	_ = json.Unmarshal(b, &m)
	return m
}
