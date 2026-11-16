package handlers

import (
	"context"
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

func (h *Handlers) unitRewriteState(ctx context.Context) unitRewriteState {
	st := unitRewriteState{UnitRewriteState: selfupdate.UnitRewriteState{
		Phase: selfupdate.UnitPhaseIdle, Unavailable: selfupdate.UnavailableUnsupported,
	}}
	if h.UpdateInstaller != nil {
		st.UnitRewriteState = h.UpdateInstaller.UnitState()
	}
	var used []string
	if h.BackupTargets != nil {
		if list, err := h.BackupTargets.List(ctx); err == nil {
			for _, t := range list {
				if t.Type == "local" {
					if dir, err := targets.LocalDir(t.Config); err == nil {
						used = append(used, dir)
					}
				}
			}
		}
	}
	st.Dirs = []targets.DirCheck{}
	for _, d := range targets.AllowedDirs() {
		c := targets.CheckDirWritable(d)
		for _, u := range used {
			if targets.Within(d, u) {
				c.InUse = true
				break
			}
		}
		st.Dirs = append(st.Dirs, c)
	}
	return st
}

// GetUnitRewrite reports which backup directories the service can write
// to and the state of the last unit rewrite.
//
//	GET /api/v1/system/unit-rewrite
func (h *Handlers) GetUnitRewrite(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.unitRewriteState(r.Context()))
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
	writeJSON(w, http.StatusAccepted, h.unitRewriteState(r.Context()))
}
