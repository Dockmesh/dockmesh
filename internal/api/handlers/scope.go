package handlers

import (
	"context"
	"fmt"
	"net/http"

	dtypes "github.com/docker/docker/api/types"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
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

// ============================================================================
// RBAC v2 — role-level scope helpers (slice R-3)
// ============================================================================
//
// The existing canAccessHost / filterHostsByScope above implement the
// USER-level scope (scope_tags on the user record). The helpers below
// implement the new ROLE-level scope (role_scopes table) — orthogonal,
// composable, AND'd with the user-level scope at request time.
//
// Until DOCKMESH_RBAC_V2_ENFORCE flips to true, the role-scope check is
// a no-op (default-allow). User-level scope continues to enforce as
// before. After cutover, both apply: a request must pass user-level
// AND role-level scope.

// checkRoleScope is the role-level (RBAC v2) counterpart to
// canAccessHost. Returns true if the request is allowed by the caller's
// role-scope rows. Default-true short-circuits when feature flag is
// off, store is nil, role is unscoped, or role couldn't be resolved.
//
// Handlers call this AFTER any existing perm + user-scope check.
func (h *Handlers) checkRoleScope(r *http.Request, req rbac.ScopeRequest) bool {
	if !h.RBACv2Enforce {
		return true
	}
	if h.Roles == nil {
		return true
	}
	role := middleware.Role(r.Context())
	if role == "" {
		return true
	}
	return h.Roles.InScope(role, req)
}

// writeRoleScopeDenied writes a 403 + emits an auth.scope_denied audit
// entry. Callers pass the perm being checked + the ScopeRequest +
// optional human-readable resourceLabel (defaults derived from req).
func (h *Handlers) writeRoleScopeDenied(w http.ResponseWriter, r *http.Request, perm rbac.Perm, req rbac.ScopeRequest, resourceLabel string) {
	role := middleware.Role(r.Context())
	target := resourceLabel
	if target == "" {
		switch {
		case req.StackName != "" && req.HostID != "":
			target = fmt.Sprintf("stack %s on host %s", req.StackName, req.HostID)
		case req.StackName != "":
			target = "stack " + req.StackName
		case req.HostID != "":
			target = "host " + req.HostID
		default:
			target = "<unknown>"
		}
	}
	h.audit(r, audit.ActionScopeDenied, target, map[string]any{
		"role":       role,
		"permission": string(perm),
		"host_id":    req.HostID,
		"stack_name": req.StackName,
	})
	writeError(w, http.StatusForbidden, "out of scope: your role isn't scoped to this resource")
}

// containerScopeReq builds a ScopeRequest for a container-keyed handler.
// Looks up the container via docker inspect to extract the compose
// stack label; falls back to host-only scope if inspect fails.
func (h *Handlers) containerScopeReq(ctx context.Context, hostID, containerID string) rbac.ScopeRequest {
	req := rbac.ScopeRequest{HostID: hostID}
	if h.Docker != nil {
		cli := h.Docker.Raw()
		if info, err := cli.ContainerInspect(ctx, containerID); err == nil {
			if info.Config != nil && info.Config.Labels != nil {
				if proj := info.Config.Labels["com.docker.compose.project"]; proj != "" {
					req.StackName = proj
				}
			}
		}
	}
	if h.HostTags != nil && hostID != "" {
		req.HostTags = h.HostTags.Tags(hostID)
	}
	return req
}

// stackScopeReq builds a ScopeRequest for a stack-name-keyed handler.
func (h *Handlers) stackScopeReq(ctx context.Context, hostID, stackName string) rbac.ScopeRequest {
	req := rbac.ScopeRequest{HostID: hostID, StackName: stackName}
	if h.HostTags != nil && hostID != "" {
		req.HostTags = h.HostTags.Tags(hostID)
	}
	return req
}

// hostScopeReq builds a ScopeRequest for a host-keyed handler.
func (h *Handlers) hostScopeReq(ctx context.Context, hostID string) rbac.ScopeRequest {
	req := rbac.ScopeRequest{HostID: hostID}
	if h.HostTags != nil && hostID != "" {
		req.HostTags = h.HostTags.Tags(hostID)
	}
	return req
}

// roleIsUnscoped reports whether the caller's role has no role_scope
// rows — admins + freshly-created roles. List handlers can short-circuit
// the filter for these.
func (h *Handlers) roleIsUnscoped(r *http.Request) bool {
	if !h.RBACv2Enforce || h.Roles == nil {
		return true
	}
	role := middleware.Role(r.Context())
	if role == "" {
		return true
	}
	rd, ok := h.Roles.Get(role)
	if !ok {
		return true
	}
	return len(rd.Scopes) == 0
}

// filterStackListByScope returns the subset of stacks the caller's
// role-scope allows. Unscoped roles get the full list back unchanged.
// Each stack is checked with a ScopeRequest carrying its name + (if
// known) its deployment host id.
func (h *Handlers) filterStackListByScope(r *http.Request, in []stackListEntry) []stackListEntry {
	if h.roleIsUnscoped(r) {
		return in
	}
	role := middleware.Role(r.Context())
	out := make([]stackListEntry, 0, len(in))
	for _, e := range in {
		req := rbac.ScopeRequest{StackName: e.Stack.Name}
		if e.Deployment != nil {
			req.HostID = e.Deployment.HostID
			if h.HostTags != nil && req.HostID != "" {
				req.HostTags = h.HostTags.Tags(req.HostID)
			}
		}
		if h.Roles.InScope(role, req) {
			out = append(out, e)
		}
	}
	return out
}

// filterContainersByRoleScope filters a flat container list. Each
// container is matched against the caller's role-scope using its host
// + compose project label (extracted via Labels map). Containers
// without a compose project are scoped by host-only (i.e. visible to
// any role unscoped-on-stack, hidden from stack-only-scoped roles).
func (h *Handlers) filterContainersByRoleScope(r *http.Request, hostID string, in []dtypes.Container) []dtypes.Container {
	if h.roleIsUnscoped(r) {
		return in
	}
	role := middleware.Role(r.Context())
	out := make([]dtypes.Container, 0, len(in))
	var hostTags []string
	if h.HostTags != nil && hostID != "" {
		hostTags = h.HostTags.Tags(hostID)
	}
	for _, c := range in {
		req := rbac.ScopeRequest{HostID: hostID, HostTags: hostTags}
		if c.Labels != nil {
			req.StackName = c.Labels["com.docker.compose.project"]
		}
		if h.Roles.InScope(role, req) {
			out = append(out, c)
		}
	}
	return out
}

// filterHostsByRoleScope returns the subset of hosts the caller's
// role-scope allows. Unscoped roles get the full list back. Hosts that
// match by ID or by any of their tags are included.
func (h *Handlers) filterHostsByRoleScope(r *http.Request, in []host.Info) []host.Info {
	if h.roleIsUnscoped(r) {
		return in
	}
	role := middleware.Role(r.Context())
	out := make([]host.Info, 0, len(in))
	for _, hh := range in {
		req := rbac.ScopeRequest{HostID: hh.ID, HostTags: hh.Tags}
		if h.Roles.InScope(role, req) {
			out = append(out, hh)
		}
	}
	return out
}

// filterContainerRowsByRoleScope is the fan-out variant — each row
// already carries its own HostID so we look up tags per row.
func (h *Handlers) filterContainerRowsByRoleScope(r *http.Request, in []containerRow) []containerRow {
	if h.roleIsUnscoped(r) {
		return in
	}
	role := middleware.Role(r.Context())
	out := make([]containerRow, 0, len(in))
	tagCache := map[string][]string{}
	for _, row := range in {
		tags, ok := tagCache[row.HostID]
		if !ok && h.HostTags != nil && row.HostID != "" {
			tags = h.HostTags.Tags(row.HostID)
			tagCache[row.HostID] = tags
		}
		req := rbac.ScopeRequest{HostID: row.HostID, HostTags: tags}
		if row.Labels != nil {
			req.StackName = row.Labels["com.docker.compose.project"]
		}
		if h.Roles.InScope(role, req) {
			out = append(out, row)
		}
	}
	return out
}
