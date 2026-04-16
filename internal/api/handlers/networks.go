package handlers

import (
	"context"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	dtypes "github.com/docker/docker/api/types"
	"github.com/go-chi/chi/v5"
)

type networkRequest struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Labels map[string]string `json:"labels,omitempty"`
}

// networkRow is the all-mode row type for ListNetworks.
type networkRow struct {
	dtypes.NetworkResource
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
}

func (h *Handlers) ListNetworks(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")

	if host.IsAll(hostID) && h.Hosts != nil {
		targets := h.Hosts.PickAll(r.Context())
		res := host.FanOut(r.Context(), targets, func(ctx context.Context, hh host.Host) ([]networkRow, error) {
			list, err := hh.ListNetworks(ctx)
			if err != nil {
				return nil, err
			}
			rows := make([]networkRow, len(list))
			for i, n := range list {
				rows[i] = networkRow{
					NetworkResource: n,
					HostID:          hh.ID(),
					HostName:        hh.Name(),
				}
			}
			return rows, nil
		})
		writeJSON(w, http.StatusOK, res)
		return
	}

	// Single-host path via the host.Host interface (pre-P.6 this handler
	// always returned local networks regardless of the selected host).
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	nets, err := target.ListNetworks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nets)
}

func (h *Handlers) InspectNetwork(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	net, err := target.InspectNetwork(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, net)
}

func (h *Handlers) CreateNetwork(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	var req networkRequest
	if err := decodeJSON(r, &req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	resp, err := h.Docker.CreateNetwork(r.Context(), req.Name, req.Driver, req.Labels)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionNetworkCreate, req.Name, nil)
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handlers) RemoveNetwork(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.Docker.RemoveNetwork(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionNetworkRemove, id, nil)
	w.WriteHeader(http.StatusNoContent)
}
