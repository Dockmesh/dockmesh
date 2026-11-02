package handlers

import (
	"context"
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
// own container; a container that can't be inspected is not.
func (h *Handlers) inspectOwn(ctx context.Context, hostID, containerID string) bool {
	target, err := h.hostByID(hostID)
	if err != nil {
		return false
	}
	info, err := target.InspectContainer(ctx, containerID)
	if err != nil {
		return false
	}
	return isOwnContainer(hostID, &info)
}

// requireNotOwnContainer writes 403 and returns false when perm acts on
// one of Dockmesh's own containers and the caller may not.
func (h *Handlers) requireNotOwnContainer(w http.ResponseWriter, r *http.Request, perm rbac.Perm, hostID, containerID string) bool {
	if !ownContainerGuarded(perm) || !h.inspectOwn(r.Context(), hostID, containerID) {
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

// requireVolumeNotOwn keeps Dockmesh's own volumes (browse, backup
// source) to admins.
func (h *Handlers) requireVolumeNotOwn(w http.ResponseWriter, r *http.Request, name string) bool {
	if !ownVolumes[name] || h.callerIsAdmin(r) {
		return true
	}
	writeError(w, http.StatusForbidden, "only an admin can access Dockmesh's own volumes")
	return false
}
