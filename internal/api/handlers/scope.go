package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// Scope checks test the permissions the route requires (recorded by
// middleware.RequirePerm), plus any the handler adds, on the resource the
// request acts on. A caller may hold a permission on one host and not on
// another, so a route-level permission check alone is never enough for
// a handler that touches a concrete host or stack.

// scopePerms are the permissions a scope check in this request tests.
// Base rights hold everywhere, so they say nothing about a resource and
// are left out: a route gated only by one needs an assignment that
// reaches the resource.
func scopePerms(r *http.Request, extra ...rbac.Perm) []rbac.Perm {
	var out []rbac.Perm
	for _, p := range append(append([]rbac.Perm(nil), middleware.RoutePerms(r.Context())...), extra...) {
		if p != "" && !rbac.IsBase(p) && !containsPerm(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func containsPerm(ps []rbac.Perm, p rbac.Perm) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}

// allowedOn reports whether the caller holds every scope permission of
// the request on res. Routes without a permission (self-service, live
// feeds) need some assignment that reaches res.
func (h *Handlers) allowedOn(r *http.Request, res rbac.Resource, extra ...rbac.Perm) bool {
	acc := middleware.AccessOf(r.Context())
	perms := scopePerms(r, extra...)
	if len(perms) == 0 {
		return acc.Covers(res)
	}
	for _, p := range perms {
		if !acc.AllowsOn(p, res) {
			return false
		}
	}
	return true
}

func (h *Handlers) hostTags(hostID string) []string {
	if h.HostTags == nil || hostID == "" {
		return nil
	}
	return h.HostTags.Tags(hostID)
}

// canAccessHost reports whether the caller holds the request's
// permissions on hostID itself or on a stack that may run there. It only
// pre-filters: handlers that act on a container or stack still check that
// exact resource, and host-level resources use inReadScope / hostVisible.
func (h *Handlers) canAccessHost(r *http.Request, hostID string) bool {
	acc := middleware.AccessOf(r.Context())
	tags := h.hostTags(hostID)
	perms := scopePerms(r)
	if len(perms) == 0 {
		return acc.CoversNear(hostID, tags)
	}
	for _, p := range perms {
		if !acc.AllowsNear(p, hostID, tags) {
			return false
		}
	}
	return true
}

// requireHostAccess writes 403 and returns false unless canAccessHost.
func (h *Handlers) requireHostAccess(w http.ResponseWriter, r *http.Request, hostID string) bool {
	if h.canAccessHost(r, hostID) {
		return true
	}
	writeError(w, http.StatusForbidden, "out of scope: none of your assignments covers this host")
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

// filterHostsByScope keeps the hosts canAccessHost lets through, for
// fan-out list handlers that filter each item afterwards.
func (h *Handlers) filterHostsByScope(r *http.Request, hosts []host.Host) []host.Host {
	if h.callerUnscoped(r) {
		return hosts
	}
	out := make([]host.Host, 0, len(hosts))
	for _, hh := range hosts {
		if h.canAccessHost(r, hh.ID()) {
			out = append(out, hh)
		}
	}
	return out
}

// writeScopeDenied audits an auth.scope_denied entry and writes 403.
func (h *Handlers) writeScopeDenied(w http.ResponseWriter, r *http.Request, perm rbac.Perm, req rbac.Resource, resourceLabel string) {
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
		"permission": string(perm),
		"host_id":    req.HostID,
		"stack_name": req.StackName,
	})
	writeError(w, http.StatusForbidden, "out of scope: none of your assignments allows this here")
}

// containerScopeReq builds the resource of a container: its host and, from
// the compose project label, its stack. If the container can't be
// inspected the request stays host-only, which a stack scope does not match.
func (h *Handlers) containerScopeReq(ctx context.Context, hostID, containerID string) rbac.Resource {
	req := rbac.Resource{HostID: hostID, HostTags: h.hostTags(hostID)}
	if target, err := h.hostByID(hostID); err == nil {
		if info, err := target.InspectContainer(ctx, containerID); err == nil && info.Config != nil {
			req.StackName = info.Config.Labels["com.docker.compose.project"]
		}
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

// requireContainerScope checks perm and the request's permissions on the
// container. Writes 403 and returns false when out of scope.
func (h *Handlers) requireContainerScope(w http.ResponseWriter, r *http.Request, perm rbac.Perm, hostID, containerID string) bool {
	scopeReq := h.containerScopeReq(r.Context(), hostID, containerID)
	if !h.allowedOn(r, scopeReq, perm) {
		h.writeScopeDenied(w, r, perm, scopeReq, "container "+containerID)
		return false
	}
	return true
}

// requireStackScope is requireContainerScope for a stack on hostID.
func (h *Handlers) requireStackScope(w http.ResponseWriter, r *http.Request, perm rbac.Perm, hostID, stackName string) bool {
	scopeReq := h.stackScopeReq(r.Context(), hostID, stackName)
	if !h.allowedOn(r, scopeReq, perm) {
		h.writeScopeDenied(w, r, perm, scopeReq, "")
		return false
	}
	return true
}

// inReadScope reports whether a resource on hostID, optionally owned by
// stack, is visible under the request's permissions. Host-level resources
// pass stack == "", so a stack scope sees none of them.
func (h *Handlers) inReadScope(r *http.Request, hostID, stack string) bool {
	return h.allowedOn(r, h.stackScopeReq(r.Context(), hostID, stack))
}

// hostsInReadScope keeps the hosts whose host-level data the caller sees.
func (h *Handlers) hostsInReadScope(r *http.Request, hosts []host.Host) []host.Host {
	out := make([]host.Host, 0, len(hosts))
	for _, hh := range hosts {
		if h.inReadScope(r, hh.ID(), "") {
			out = append(out, hh)
		}
	}
	return out
}

// callerUnscoped reports whether every permission the caller holds is
// held on everything, so lists need no per-item filtering.
func (h *Handlers) callerUnscoped(r *http.Request) bool {
	return middleware.AccessOf(r.Context()).Unscoped()
}

// stackScopeReq builds the resource of a stack on hostID ("" when it is not
// deployed).
func (h *Handlers) stackScopeReq(_ context.Context, hostID, stackName string) rbac.Resource {
	return rbac.Resource{HostID: hostID, StackName: stackName, HostTags: h.hostTags(hostID)}
}

// hostScopeReq builds the resource of a host.
func (h *Handlers) hostScopeReq(_ context.Context, hostID string) rbac.Resource {
	return rbac.Resource{HostID: hostID, HostTags: h.hostTags(hostID)}
}

// StackScope resolves the stack of /stacks/{name}/... requests: deployed
// stacks are checked on their deployment host. A stack that is not
// deployed has no host, which only assignments on everything or on that
// stack without a host reach, for reads and writes alike: a host scope
// must not let anyone rewrite or deploy a stack staged for another host.
// The two status probes expose no files and only need the route's
// permission somewhere. RequirePerm then tests the route's permission on
// the resolved resource.
func (h *Handlers) StackScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		rctx := chi.RouteContext(r.Context())
		if name == "" || rctx == nil || !strings.HasPrefix(rctx.RoutePattern(), "/api/v1/stacks/{name}") || h.Deployments == nil {
			next.ServeHTTP(w, r)
			return
		}
		d, err := h.Deployments.Get(r.Context(), name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		hostID := ""
		if d != nil {
			hostID = d.HostID
		}
		if hostID == "" && undeployedProbe(rctx.RoutePattern()) {
			next.ServeHTTP(w, r)
			return
		}
		res := h.stackScopeReq(r.Context(), hostID, name)
		if !middleware.AccessOf(r.Context()).Covers(res) {
			h.writeScopeDenied(w, r, rbac.PermStacksView, res, "")
			return
		}
		next.ServeHTTP(w, r.WithContext(middleware.WithResource(r.Context(), res)))
	})
}

// undeployedProbe matches the two read routes that expose no stack files,
// which clients poll right after creating a stack.
func undeployedProbe(pattern string) bool {
	return strings.HasSuffix(pattern, "/status") || strings.HasSuffix(pattern, "/deploy/progress")
}

// filterStackListByScope keeps the stacks the caller may view: deployed
// stacks on their host, others only through an assignment on everything
// or on that stack.
func (h *Handlers) filterStackListByScope(r *http.Request, in []stackListEntry) []stackListEntry {
	if h.callerUnscoped(r) {
		return in
	}
	out := make([]stackListEntry, 0, len(in))
	for _, e := range in {
		hostID := ""
		if e.Deployment != nil {
			hostID = e.Deployment.HostID
		}
		if h.allowedOn(r, h.stackScopeReq(r.Context(), hostID, e.Name)) {
			out = append(out, e)
		}
	}
	return out
}

// filterContainersByRoleScope filters the containers of one host by their
// compose project. Containers outside a stack are host-level.
func (h *Handlers) filterContainersByRoleScope(r *http.Request, hostID string, in []container.Summary) []container.Summary {
	if h.callerUnscoped(r) {
		return in
	}
	out := make([]container.Summary, 0, len(in))
	tags := h.hostTags(hostID)
	for _, c := range in {
		req := rbac.Resource{HostID: hostID, HostTags: tags}
		if c.Labels != nil {
			req.StackName = c.Labels["com.docker.compose.project"]
		}
		if h.allowedOn(r, req) {
			out = append(out, c)
		}
	}
	return out
}

// filterHostsByRoleScope keeps the hosts the caller sees as hosts.
func (h *Handlers) filterHostsByRoleScope(r *http.Request, in []host.Info) []host.Info {
	if h.callerUnscoped(r) {
		return in
	}
	out := make([]host.Info, 0, len(in))
	for _, hh := range in {
		if h.allowedOn(r, rbac.Resource{HostID: hh.ID, HostTags: hh.Tags}) {
			out = append(out, hh)
		}
	}
	return out
}

// filterContainerRowsByRoleScope is the fan-out variant — each row
// already carries its own HostID so we look up tags per row.
func (h *Handlers) filterContainerRowsByRoleScope(r *http.Request, in []containerRow) []containerRow {
	if h.callerUnscoped(r) {
		return in
	}
	out := make([]containerRow, 0, len(in))
	tagCache := map[string][]string{}
	for _, row := range in {
		tags, ok := tagCache[row.HostID]
		if !ok {
			tags = h.hostTags(row.HostID)
			tagCache[row.HostID] = tags
		}
		req := rbac.Resource{HostID: row.HostID, HostTags: tags}
		if row.Labels != nil {
			req.StackName = row.Labels["com.docker.compose.project"]
		}
		if h.allowedOn(r, req) {
			out = append(out, row)
		}
	}
	return out
}

// hostVisible reports whether the request's permissions hold on the host
// itself, the rule /hosts filters by.
func (h *Handlers) hostVisible(r *http.Request, id string) bool {
	return h.allowedOn(r, h.hostScopeReq(r.Context(), id))
}

// stackRes is the resource of stack name: the one StackScope resolved for
// this request, or the stack on its deployment host.
func (h *Handlers) stackRes(r *http.Request, name string) rbac.Resource {
	if res, ok := middleware.ResourceOf(r.Context()); ok && res.StackName == name {
		return res
	}
	hostID := ""
	if h.Deployments != nil {
		if d, err := h.Deployments.Get(r.Context(), name); err == nil && d != nil {
			hostID = d.HostID
		}
	}
	return h.stackScopeReq(r.Context(), hostID, name)
}
