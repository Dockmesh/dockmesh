package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// canAccessHost combines the caller's scope (from JWT context) with the
// target host's tags (from the hosttags service) and returns true if
// the caller is allowed to act on the host.
//
// Rules:
//   - Empty scope → all hosts accessible (backward-compatible default
//     for users who haven't been explicitly scoped).
//   - Non-empty scope → host must have at least one tag that matches.
//
// Admin users are NOT special-cased here — if an admin has been given
// a scope, it applies. Admins without scope retain global access
// (which is the norm). This makes it possible to sandbox even admins
// when needed (e.g. a per-team-admin pattern).
func (h *Handlers) canAccessHost(r *http.Request, hostID string) bool {
	scope := middleware.ScopeTags(r.Context())
	if len(scope) == 0 {
		return true
	}
	if h.HostTags == nil {
		// If tags aren't available, refuse — safer than leaking access.
		// This only hits if the server boots without the hosttags
		// service, which shouldn't happen in prod.
		return false
	}
	return rbac.ScopeMatchesHost(scope, h.HostTags.Tags(hostID))
}

// requireHostAccess is a one-liner used at the top of handlers that act
// on a specific host. Writes 403 and returns false if the caller is
// out-of-scope; returns true otherwise and the caller proceeds.
//
// Usage:
//
//	if !h.requireHostAccess(w, r, hostID) {
//	    return
//	}
func (h *Handlers) requireHostAccess(w http.ResponseWriter, r *http.Request, hostID string) bool {
	if h.canAccessHost(r, hostID) {
		return true
	}
	writeError(w, http.StatusForbidden, "out of scope: your role does not include this host")
	return false
}

// hostIDFromRequest pulls the target host id from the ?host= query
// parameter, treating empty/missing as "local". Matches pickHost's
// resolution rules so callers can use either helper interchangeably.
func hostIDFromRequest(r *http.Request) string {
	id := r.URL.Query().Get("host")
	if id == "" || id == "all" {
		return "local"
	}
	return id
}

// filterHostsByScope returns the subset of hosts the caller is allowed
// to see under their current scope. Empty scope = full list unchanged.
// Used by fan-out list handlers to drop out-of-scope hosts from
// aggregate views (ListContainers in all-mode, Images, Volumes, etc.).
//
// Reads are silently filtered; mutations use requireHostAccess instead
// and surface a 403.
func (h *Handlers) filterHostsByScope(r *http.Request, hosts []host.Host) []host.Host {
	scope := middleware.ScopeTags(r.Context())
	if len(scope) == 0 || h.HostTags == nil {
		return hosts
	}
	out := make([]host.Host, 0, len(hosts))
	for _, hh := range hosts {
		if rbac.ScopeMatchesHost(scope, h.HostTags.Tags(hh.ID())) {
			out = append(out, hh)
		}
	}
	return out
}
