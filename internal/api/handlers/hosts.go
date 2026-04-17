package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// ListHosts returns the local docker daemon plus every registered agent
// (online or offline) for the frontend host switcher. Each entry is
// decorated with its tag list so the UI can render chips without a
// second round-trip per host.
//
// Scoped users (P.11.3) see only hosts whose tags match their scope.
// An unscoped user (default) sees everything.
func (h *Handlers) ListHosts(w http.ResponseWriter, r *http.Request) {
	if h.Hosts == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	list, err := h.Hosts.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Decorate with tags.
	if h.HostTags != nil {
		for i := range list {
			list[i].Tags = h.HostTags.Tags(list[i].ID)
		}
	}
	// Apply scope filter. Empty scope → no-op.
	if scope := middleware.ScopeTags(r.Context()); len(scope) > 0 {
		filtered := make([]host.Info, 0, len(list))
		for _, info := range list {
			if rbac.ScopeMatchesHost(scope, info.Tags) {
				filtered = append(filtered, info)
			}
		}
		list = filtered
	}
	writeJSON(w, http.StatusOK, list)
}
