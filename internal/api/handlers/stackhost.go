package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// pickStackHost is the host a request about one stack acts on: the host
// named in ?host=, otherwise the stack's own host, otherwise local. Without
// the fallback a call that leaves out ?host= would run an agent's stack on
// the server. A stack deployed to a Swarm cluster has no host to act on:
// stacks.ErrClusterTarget, which writeStackTargetError answers with 409.
func (h *Handlers) pickStackHost(r *http.Request, name string) (host.Host, error) {
	if h.Deployments != nil {
		t, err := h.Deployments.TargetOf(r.Context(), name)
		if err != nil {
			return nil, err
		}
		if t.Kind == stacks.TargetCluster {
			return nil, stacks.ErrClusterTarget
		}
		if r.URL.Query().Get("host") == "" && h.Hosts != nil && t.ID != "" {
			return h.Hosts.Pick(t.ID)
		}
	}
	return h.pickHost(r)
}

// writeStackTargetError answers a failed pickStackHost or HostOf.
func writeStackTargetError(w http.ResponseWriter, err error) {
	if errors.Is(err, stacks.ErrClusterTarget) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "stack_on_cluster"})
		return
	}
	writeError(w, http.StatusServiceUnavailable, err.Error())
}

// refuseOtherHost answers 409 when a deploy, stop, scale or rollback names
// a host other than the one the stack is deployed or stopped on. Running it
// there starts a second copy with empty volumes and moves the deployment
// record away from the real one; moving a stack is what Migrate is for.
func (h *Handlers) refuseOtherHost(w http.ResponseWriter, r *http.Request, name string, target host.Host) bool {
	if h.Deployments == nil {
		return false
	}
	d, err := h.Deployments.Get(r.Context(), name)
	if err != nil || d == nil || d.HostID == "" || d.HostID == target.ID() {
		return false
	}
	switch d.Status {
	case "deployed", "stopped", "migrating", stacks.DeploymentDeletePending:
	default:
		return false
	}
	body := map[string]any{
		"error": "stack " + name + " is on another host; run this there, or migrate the stack to move it",
		"code":  "stack_on_other_host",
		"stack": name,
	}
	if h.hostVisible(r, d.HostID) {
		where := d.HostID
		if h.Hosts != nil {
			if home, err := h.Hosts.Pick(d.HostID); err == nil {
				where = home.Name()
			}
		}
		body["error"] = "stack " + name + " is on host " + where + "; run this there, or migrate the stack to move it"
		body["host_id"] = d.HostID
	}
	writeJSON(w, http.StatusConflict, body)
	return true
}
