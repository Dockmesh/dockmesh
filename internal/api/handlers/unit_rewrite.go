package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
)

// unitRewriteState is the answer of the unit rewrite endpoints: the
// rewrite's state plus whether the service can write to each directory
// local backups may use.
type unitRewriteState struct {
	selfupdate.UnitRewriteState
	Dirs []targets.DirCheck `json:"dirs"`
}

func (h *Handlers) unitRewriteState() unitRewriteState {
	st := unitRewriteState{UnitRewriteState: selfupdate.UnitRewriteState{
		Phase: selfupdate.UnitPhaseIdle, Unavailable: selfupdate.UnavailableUnsupported,
	}}
	if h.UpdateInstaller != nil {
		st.UnitRewriteState = h.UpdateInstaller.UnitState()
	}
	st.Dirs = []targets.DirCheck{}
	for _, d := range targets.AllowedDirs() {
		st.Dirs = append(st.Dirs, targets.CheckDirWritable(d))
	}
	return st
}

// GetUnitRewrite reports which backup directories the service can write
// to and the state of the last unit rewrite.
//
//	GET /api/v1/system/unit-rewrite
func (h *Handlers) GetUnitRewrite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.unitRewriteState())
}

// StartUnitRewrite asks the root updater to write the service unit again
// from the settings and restart the server, so directories added to the
// backup settings become writable.
//
//	POST /api/v1/system/unit-rewrite
func (h *Handlers) StartUnitRewrite(w http.ResponseWriter, r *http.Request) {
	if h.UpdateInstaller == nil {
		writeError(w, http.StatusConflict, selfupdate.ErrUpdaterMissing.Error())
		return
	}
	err := h.UpdateInstaller.StartUnitRewrite(r.Context())
	switch {
	case errors.Is(err, selfupdate.ErrUnitRewriteRunning), errors.Is(err, selfupdate.ErrInstallRunning),
		errors.Is(err, selfupdate.ErrUpdaterMissing), errors.Is(err, selfupdate.ErrInContainer):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "system.unit_rewrite", "", nil)
	writeJSON(w, http.StatusAccepted, h.unitRewriteState())
}
