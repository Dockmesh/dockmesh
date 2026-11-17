package handlers

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/setup"
	"github.com/dockmesh/dockmesh/internal/system"
)

// agentDefaultDataDir is the data dir install-agent.sh picks, for agents
// that do not report theirs.
const agentDefaultDataDir = "/var/lib/dockmesh-agent"

// stacksParents are where the agent's root updater creates a new stacks
// directory one level down; a disk mounted at one of them, or one level
// below, gets <mount>/stacks. The agent checks again on apply.
var stacksParents = []string{"/srv", "/opt", "/data", "/mnt", "/media"}

// hostSetupAccess runs the checks the stacks-dir routes of a remote host
// share and reports whether the request may go on.
func (h *Handlers) hostSetupAccess(w http.ResponseWriter, r *http.Request, id string) bool {
	if h.Agents == nil {
		writeError(w, http.StatusServiceUnavailable, "agents not configured")
		return false
	}
	scopeReq := h.hostScopeReq(id)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermHostsUpdate, scopeReq, "host "+id)
		return false
	}
	if !h.requireHostAccess(w, r, id) {
		return false
	}
	if _, err := h.Agents.Get(r.Context(), id); errors.Is(err, agents.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	return true
}

// CheckHostStacksDir checks a stacks directory on the host with the rules
// its root updater applies, without changing anything.
//
//	POST /api/v1/hosts/{id}/stacks-dir/check
func (h *Handlers) CheckHostStacksDir(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.hostSetupAccess(w, r, id) {
		return
	}
	var req setStacksDirReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	res, err := h.Agents.CheckStacksDir(r.Context(), id, req.Path)
	switch {
	case errors.Is(err, agents.ErrInvalidStacksDir):
		writeError(w, http.StatusBadRequest, msgInvalidStacksDir)
	case errors.Is(err, agents.ErrAgentNotConnected), errors.Is(err, agents.ErrAgentGone):
		writeError(w, http.StatusConflict, "agent not connected")
	case errors.Is(err, agents.ErrLegacyLayout), errors.Is(err, agents.ErrStacksDirCheckRunning):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

// ListHostStacksDirDisks lists the host's disks with a suggested stacks
// directory on each one that can take it.
//
//	GET /api/v1/hosts/{id}/stacks-dir/disks
func (h *Handlers) ListHostStacksDirDisks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.hostSetupAccess(w, r, id) {
		return
	}
	hh, err := h.Hosts.Pick(id)
	if err != nil {
		writeError(w, http.StatusConflict, "agent not connected")
		return
	}
	dm, ok := hh.(interface {
		DiskMounts(context.Context) ([]system.Mount, error)
	})
	if !ok {
		writeError(w, http.StatusConflict, "agent not connected")
		return
	}
	mounts, err := dm.DiskMounts(r.Context())
	switch {
	case errors.Is(err, host.ErrMountsUnsupported):
		writeJSON(w, http.StatusOK, map[string]any{"disks": []setup.Disk{}, "unsupported": true})
		return
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	dataDir := agentDefaultDataDir
	if a, err := h.Agents.Get(r.Context(), id); err == nil && a.DataDir != "" {
		dataDir = a.DataDir
	}
	writeJSON(w, http.StatusOK, map[string]any{"disks": stacksDirDisks(mounts, dataDir)})
}

// stacksDirDisks turns the host's mounts into the setup disk list: the
// root filesystem suggests the default <data>/stacks, a disk the root
// updater may create a directory on suggests <mount>/stacks.
func stacksDirDisks(mounts []system.Mount, dataDir string) []setup.Disk {
	out := []setup.Disk{}
	seen := map[string]bool{}
	for _, m := range mounts {
		mp := m.MountPoint
		if mp == "" || seen[mp] || setup.PseudoFS(m.FSType) || m.Error != "" || dockerOwned(m) {
			continue
		}
		seen[mp] = true
		d := setup.Disk{Mountpoint: mp, FSType: m.FSType, TotalBytes: int64(min(m.Total, 1<<62)), Usable: true}
		if m.Total >= m.Used {
			d.FreeBytes = int64(min(m.Total-m.Used, 1<<62))
		}
		d.Suggested = path.Join(mp, "stacks")
		if mp == "/" {
			d.Suggested = path.Join(dataDir, "stacks")
		} else if !underStacksParent(mp) {
			d.Usable, d.Reason = false, "not allowed as a stacks dir"
		}
		switch {
		case setup.NetworkFS(m.FSType):
			d.Usable, d.Reason = false, "network share"
		case setup.NoUnixPermsFS(m.FSType):
			d.Usable, d.Reason = false, "no Unix permissions"
		}
		out = append(out, d)
	}
	return out
}

func underStacksParent(mp string) bool {
	return slices.Contains(stacksParents, mp) || slices.Contains(stacksParents, path.Dir(mp))
}

// dockerOwned reports Docker's own directories, which are not offered.
func dockerOwned(m system.Mount) bool {
	return m.MountPoint == "/var/lib/docker" || strings.HasPrefix(m.MountPoint, "/var/lib/docker/")
}
