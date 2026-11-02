package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/docker/docker/api/types/container"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Dockmesh's own containers are the server (when it runs in a
// container), the reverse proxy, agents and short-lived helpers. They
// hold the secret key, the database, the proxy's TLS keys and agent
// certificates, and stopping one takes Dockmesh down, so only admins act
// on them and nobody reads their files through the file browser.

// ownImagePrefix is the registry path of the server and agent images.
const ownImagePrefix = "ghcr.io/dockmesh/"

// ownVolumes hold the proxy's config and ACME/TLS private keys and an
// agent's client certificate.
var ownVolumes = map[string]bool{
	"dockmesh-caddy-data":   true,
	"dockmesh-caddy-config": true,
	"dockmesh-agent":        true,
}

// isOwnContainer reports whether info is one of Dockmesh's own containers
// on hostID.
func isOwnContainer(hostID string, info *container.InspectResponse) bool {
	if info == nil {
		return false
	}
	if hostID == "local" || hostID == "" {
		if self := host.SelfContainerID(); self != "" && info.ID == self {
			return true
		}
	}
	if info.Config == nil {
		return false
	}
	return info.Config.Labels["dockmesh.component"] != "" || strings.HasPrefix(info.Config.Image, ownImagePrefix)
}

// isOwnSummary is isOwnContainer for a container list entry.
func isOwnSummary(hostID string, c container.Summary) bool {
	if hostID == "local" || hostID == "" {
		if self := host.SelfContainerID(); self != "" && c.ID == self {
			return true
		}
	}
	return c.Labels["dockmesh.component"] != "" || strings.HasPrefix(c.Image, ownImagePrefix)
}

// projectHasOwnContainer reports whether the compose project on hostID
// runs one of Dockmesh's own containers, as the shipped compose file does
// for the server. Stopping, scaling or redeploying such a project stops
// Dockmesh itself.
func (h *Handlers) projectHasOwnContainer(ctx context.Context, hostID, project string) bool {
	if hostID == "" {
		hostID = "local"
	}
	target, err := h.hostByID(hostID)
	if err != nil {
		return false
	}
	cs, err := target.ListContainers(ctx, true)
	if err != nil {
		return false
	}
	for _, c := range cs {
		if c.Labels["com.docker.compose.project"] == project && isOwnSummary(hostID, c) {
			return true
		}
	}
	return false
}

// ownContainerGuarded lists what non-admins may not do to an own
// container. Files are refused for everyone.
func ownContainerGuarded(perm rbac.Perm) bool {
	switch perm {
	case rbac.PermContainersUpdate, rbac.PermContainersDelete, rbac.PermContainersExec,
		rbac.PermContainersUpgrade, rbac.PermContainersFiles:
		return true
	}
	return false
}

// inspectOwn inspects containerID on hostID and reports whether it is an
// own container. A host that can't be resolved or has no Docker is not an
// error here: the action itself can't reach it either and reports why.
func (h *Handlers) inspectOwn(ctx context.Context, hostID, containerID string) (bool, error) {
	target, err := h.hostByID(hostID)
	if err != nil {
		return false, nil
	}
	info, err := target.InspectContainer(ctx, containerID)
	if errors.Is(err, host.ErrNoDocker) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return isOwnContainer(hostID, &info), nil
}

// requireNotOwnContainer writes 403 and returns false when perm acts on
// one of Dockmesh's own containers and the caller may not. A container
// that can't be inspected is refused to non-admins, so a timeout doesn't
// let a retry through.
func (h *Handlers) requireNotOwnContainer(w http.ResponseWriter, r *http.Request, perm rbac.Perm, hostID, containerID string) bool {
	if !ownContainerGuarded(perm) {
		return true
	}
	own, err := h.inspectOwn(r.Context(), hostID, containerID)
	if err != nil {
		if perm != rbac.PermContainersFiles && h.callerIsAdmin(r) {
			return true
		}
		writeError(w, http.StatusNotFound, "container not found or not reachable")
		return false
	}
	if !own {
		return true
	}
	if perm == rbac.PermContainersFiles {
		writeError(w, http.StatusForbidden, "the file browser is not available for Dockmesh's own containers")
		return false
	}
	if h.callerIsAdmin(r) {
		return true
	}
	writeError(w, http.StatusForbidden, "only an admin can change or open a shell in Dockmesh's own containers")
	return false
}

// requireVolumeNotOwn keeps Dockmesh's own volumes (browse, removal,
// backup source and restore target) to admins.
func (h *Handlers) requireVolumeNotOwn(w http.ResponseWriter, r *http.Request, name string) bool {
	if !ownVolumes[name] || h.callerIsAdmin(r) {
		return true
	}
	writeError(w, http.StatusForbidden, "only an admin can access Dockmesh's own volumes")
	return false
}
