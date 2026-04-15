package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	dtypes "github.com/docker/docker/api/types"
	"github.com/go-chi/chi/v5"
)

// containerRow is the all-mode row for ListContainers. The embedded
// dtypes.Container marshals its fields flat alongside host_id / host_name
// so the frontend reads everything off one JSON object instead of having
// to follow a .row indirection.
type containerRow struct {
	dtypes.Container
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
}

func (h *Handlers) ListContainers(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	hostID := r.URL.Query().Get("host")

	// All-mode: fan out across every online host, collect rows with per-
	// row host metadata, report unreachable hosts in a structured way so
	// the frontend can show "Showing data from N of M hosts".
	if host.IsAll(hostID) {
		if h.Hosts == nil {
			// No registry wired — legacy single-host boot. Degrade to local.
			hostID = "local"
		} else {
			targets := h.Hosts.PickAll(r.Context())
			res := host.FanOut(r.Context(), targets, func(ctx context.Context, hh host.Host) ([]containerRow, error) {
				list, err := hh.ListContainers(ctx, all)
				if err != nil {
					return nil, err
				}
				rows := make([]containerRow, len(list))
				for i, c := range list {
					rows[i] = containerRow{
						Container: c,
						HostID:    hh.ID(),
						HostName:  hh.Name(),
					}
				}
				return rows, nil
			})
			writeJSON(w, http.StatusOK, res)
			return
		}
	}

	// Single-host path — unchanged shape, unchanged single-host callers.
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	list, err := target.ListContainers(r.Context(), all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) InspectContainer(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	info, err := target.InspectContainer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (h *Handlers) StartContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "start", audit.ActionContainerStart)
}

func (h *Handlers) StopContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "stop", audit.ActionContainerStop)
}

func (h *Handlers) RestartContainer(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "restart", audit.ActionContainerKill)
}

func (h *Handlers) RemoveContainer(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	force := r.URL.Query().Get("force") == "true"
	if err := target.RemoveContainer(r.Context(), id, force); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionContainerRm, id, map[string]string{"host": target.ID()})
	w.WriteHeader(http.StatusNoContent)
}

// containerAction dispatches a start/stop/restart op against whichever host
// (local or agent) the request is targeted at via ?host=.
func (h *Handlers) containerAction(w http.ResponseWriter, r *http.Request, op string, action string) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	switch op {
	case "start":
		err = target.StartContainer(r.Context(), id)
	case "stop":
		err = target.StopContainer(r.Context(), id)
	case "restart":
		err = target.RestartContainer(r.Context(), id)
	default:
		err = errors.New("unknown op: " + op)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, action, id, map[string]string{"host": target.ID()})
	w.WriteHeader(http.StatusNoContent)
}
