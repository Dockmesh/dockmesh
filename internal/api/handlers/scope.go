package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	dtypes "github.com/docker/docker/api/types"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
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
	if len(scope) == 0 {
		return hosts
	}
	if h.HostTags == nil {
		return []host.Host{}
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
// RBAC v2 — role-level scope helpers
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
// canAccessHost. Returns true if the request is allowed by the role-scope
// rows of every role the caller acts under. Default-true when the feature
// flag is off or the store is nil.
//
// Handlers call this AFTER any existing perm + user-scope check.
func (h *Handlers) checkRoleScope(r *http.Request, req rbac.ScopeRequest) bool {
	if !h.RBACv2Enforce {
		return true
	}
	if h.Roles == nil {
		return true
	}
	return h.inRoleScope(r, req)
}

// inRoleScope requires req to be in scope for every role in
// middleware.Roles; an empty role (unauthenticated) is allowed.
func (h *Handlers) inRoleScope(r *http.Request, req rbac.ScopeRequest) bool {
	for _, role := range middleware.Roles(r.Context()) {
		if role != "" && !h.Roles.InScope(role, req) {
			return false
		}
	}
	return true
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
// The compose stack label is read by inspecting the container on hostID,
// the host it actually runs on; if that fails the request stays host-only,
// which a stack-scoped role does not match.
func (h *Handlers) containerScopeReq(ctx context.Context, hostID, containerID string) rbac.ScopeRequest {
	req := rbac.ScopeRequest{HostID: hostID}
	if target, err := h.hostByID(hostID); err == nil {
		if info, err := target.InspectContainer(ctx, containerID); err == nil && info.Config != nil {
			req.StackName = info.Config.Labels["com.docker.compose.project"]
		}
	}
	if h.HostTags != nil && hostID != "" {
		req.HostTags = h.HostTags.Tags(hostID)
	}
	return req
}

// hostByID resolves a host id like pickHost does for ?host=.
func (h *Handlers) hostByID(hostID string) (host.Host, error) {
	if h.Hosts == nil {
		if h.Docker == nil {
			return nil, host.ErrNoDocker
		}
		return host.NewLocal(h.Docker), nil
	}
	return h.Hosts.Pick(hostID)
}

// requireContainerScope applies the caller's host scope and the role scope
// of the container on hostID. Writes 403 and returns false when out of scope.
func (h *Handlers) requireContainerScope(w http.ResponseWriter, r *http.Request, perm rbac.Perm, hostID, containerID string) bool {
	if !h.requireHostAccess(w, r, hostID) {
		return false
	}
	scopeReq := h.containerScopeReq(r.Context(), hostID, containerID)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, perm, scopeReq, "container "+containerID)
		return false
	}
	return true
}

// requireStackScope is requireContainerScope for a stack on hostID.
func (h *Handlers) requireStackScope(w http.ResponseWriter, r *http.Request, perm rbac.Perm, hostID, stackName string) bool {
	if !h.requireHostAccess(w, r, hostID) {
		return false
	}
	scopeReq := h.stackScopeReq(r.Context(), hostID, stackName)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, perm, scopeReq, "")
		return false
	}
	return true
}

// inReadScope reports whether a resource on hostID, optionally owned by
// stack, is visible to the caller under both host scope and role scope.
// Host-level resources pass stack == "", so a role scoped only to stacks
// sees none of them.
func (h *Handlers) inReadScope(r *http.Request, hostID, stack string) bool {
	if !h.canAccessHost(r, hostID) {
		return false
	}
	if !h.RBACv2Enforce || h.Roles == nil {
		return true
	}
	return h.inRoleScope(r, h.stackScopeReq(r.Context(), hostID, stack))
}

// callerUnscoped reports whether the caller has neither a host scope nor
// a role scope, i.e. sees every host and stack.
func (h *Handlers) callerUnscoped(r *http.Request) bool {
	return len(middleware.ScopeTags(r.Context())) == 0 && h.roleIsUnscoped(r)
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

// roleIsUnscoped reports whether none of the roles the caller acts under
// has role_scope rows — admins + freshly-created roles. List handlers can short-circuit
// the filter for these.
func (h *Handlers) roleIsUnscoped(r *http.Request) bool {
	if !h.RBACv2Enforce || h.Roles == nil {
		return true
	}
	for _, role := range middleware.Roles(r.Context()) {
		if role == "" {
			continue
		}
		if rd, ok := h.Roles.Get(role); ok && len(rd.Scopes) > 0 {
			return false
		}
	}
	return true
}

// StackScope guards every /stacks/{name}/... route: once a stack is
// deployed, only callers whose host scope and role scope include it on its
// deployment host reach the handler. Undeployed stacks are left to the
// handler's own checks against the host it is asked to act on.
func (h *Handlers) StackScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		rctx := chi.RouteContext(r.Context())
		if name == "" || rctx == nil || !strings.HasPrefix(rctx.RoutePattern(), "/api/v1/stacks/{name}") ||
			h.Deployments == nil || h.callerUnscoped(r) {
			next.ServeHTTP(w, r)
			return
		}
		d, err := h.Deployments.Get(r.Context(), name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if d != nil && d.HostID != "" && !h.requireStackScope(w, r, rbac.PermStacksView, d.HostID, name) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// filterStackListByScope returns the subset of stacks the caller's host
// scope and role scope allow. Each stack is checked with its name and,
// when deployed, its deployment host.
func (h *Handlers) filterStackListByScope(r *http.Request, in []stackListEntry) []stackListEntry {
	if h.callerUnscoped(r) {
		return in
	}
	out := make([]stackListEntry, 0, len(in))
	for _, e := range in {
		if e.Deployment != nil && e.Deployment.HostID != "" && !h.canAccessHost(r, e.Deployment.HostID) {
			continue
		}
		req := rbac.ScopeRequest{StackName: e.Stack.Name}
		if e.Deployment != nil {
			req.HostID = e.Deployment.HostID
			if h.HostTags != nil && req.HostID != "" {
				req.HostTags = h.HostTags.Tags(req.HostID)
			}
		}
		if h.checkRoleScope(r, req) {
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
		if h.inRoleScope(r, req) {
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
	out := make([]host.Info, 0, len(in))
	for _, hh := range in {
		req := rbac.ScopeRequest{HostID: hh.ID, HostTags: hh.Tags}
		if h.inRoleScope(r, req) {
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
		if h.inRoleScope(r, req) {
			out = append(out, row)
		}
	}
	return out
}
