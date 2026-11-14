package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/selfupdate"
)

type installUpdateRequest struct {
	Version string `json:"version"`
}

// InstallUpdate downloads a release and hands it to the root updater,
// which restarts the server on the new version or rolls it back.
func (h *Handlers) InstallUpdate(w http.ResponseWriter, r *http.Request) {
	if h.UpdateInstaller == nil {
		writeError(w, http.StatusConflict, selfupdate.ErrUpdaterMissing.Error())
		return
	}
	var req installUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	err := h.UpdateInstaller.Start(r.Context(), req.Version)
	switch {
	case errors.Is(err, selfupdate.ErrInvalidVersion), errors.Is(err, selfupdate.ErrNotNewer):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, selfupdate.ErrInstallRunning), errors.Is(err, selfupdate.ErrUpdaterMissing):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "system.update_install", req.Version, nil)
	writeJSON(w, http.StatusAccepted, h.UpdateInstaller.State())
}

// GetUpdateInstall reports the install started last.
func (h *Handlers) GetUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if h.UpdateInstaller == nil {
		writeJSON(w, http.StatusOK, selfupdate.InstallState{Phase: selfupdate.PhaseIdle})
		return
	}
	writeJSON(w, http.StatusOK, h.UpdateInstaller.State())
}
