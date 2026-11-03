package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/host"
)

// pickStackHost is the host a request about one stack acts on: the host
// named in ?host=, otherwise the stack's own host, otherwise local. Without
// the fallback a call that leaves out ?host= would run an agent's stack on
// the server.
func (h *Handlers) pickStackHost(r *http.Request, name string) (host.Host, error) {
	if r.URL.Query().Get("host") == "" && h.Hosts != nil {
		if home := h.stackHost(r.Context(), name); home != "" {
			return h.Hosts.Pick(home)
		}
	}
	return h.pickHost(r)
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
	case "deployed", "stopped", "migrating":
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
