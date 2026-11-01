package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// assignmentView adds the username to an assignment.
type assignmentView struct {
	rbac.Assignment
	Username string `json:"username"`
}

func (h *Handlers) withUsernames(r *http.Request, as []rbac.Assignment) []assignmentView {
	names := map[string]string{}
	if users, err := h.Auth.ListUsers(r.Context()); err == nil {
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}
	out := make([]assignmentView, 0, len(as))
	for _, a := range as {
		out = append(out, assignmentView{Assignment: a, Username: names[a.UserID]})
	}
	return out
}

// actorID is the user an action is attributed to: the signed-in user or
// the owner of the API token in use.
func actorID(r *http.Request) string {
	if uid := middleware.UserID(r.Context()); uid != "" {
		return uid
	}
	return middleware.TokenOwnerID(r.Context())
}

func (h *Handlers) auditAssignment(r *http.Request, action, username string, a rbac.Assignment) {
	h.audit(r, action, username, map[string]any{
		"assignment": a.ID, "user_id": a.UserID, "role": a.Role, "scope": a.Scope.String(), "source": a.Source,
	})
}

// ListAssignments returns assignments, optionally of one user or role.
//
//	GET /api/v1/assignments?user=<id>&role=<name>
func (h *Handlers) ListAssignments(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	user, role := r.URL.Query().Get("user"), r.URL.Query().Get("role")
	var as []rbac.Assignment
	for _, a := range h.Roles.AllAssignments() {
		if (user == "" || a.UserID == user) && (role == "" || a.Role == role) {
			as = append(as, a)
		}
	}
	writeJSON(w, http.StatusOK, h.withUsernames(r, as))
}

type createAssignmentRequest struct {
	UserID string     `json:"user_id"`
	Role   string     `json:"role"`
	Scope  rbac.Scope `json:"scope"`
}

// CreateAssignment gives a user a role on a scope.
//
//	POST /api/v1/assignments
func (h *Handlers) CreateAssignment(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	var req createAssignmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Scope.Type == "" {
		req.Scope = rbac.Global
	}
	if err := req.Scope.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.isValidRole(req.Role) {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	u, err := h.Auth.GetUser(r.Context(), req.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := h.checkGrant(r, req.Role, req.Scope); err != nil {
		writeGrantError(w, err)
		return
	}
	a, err := h.Roles.AddAssignment(r.Context(), rbac.Assignment{UserID: u.ID, Role: req.Role, Scope: req.Scope, CreatedBy: actorID(r)})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.auditAssignment(r, audit.ActionAssignmentAdd, u.Username, a)
	writeJSON(w, http.StatusCreated, assignmentView{Assignment: a, Username: u.Username})
}

// DeleteAssignment removes an assignment. The last admin assignment on
// everything can't be removed.
//
//	DELETE /api/v1/assignments/{id}
func (h *Handlers) DeleteAssignment(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	a, ok := h.Roles.Assignment(id)
	if !ok {
		writeError(w, http.StatusNotFound, "assignment not found")
		return
	}
	if err := h.checkGrant(r, a.Role, a.Scope); err != nil {
		writeGrantError(w, err)
		return
	}
	if err := h.checkKeepsAdmin(func(x rbac.Assignment) bool { return x.ID == id }); err != nil {
		writeLastAdminError(w, err)
		return
	}
	if err := h.Roles.RemoveAssignment(r.Context(), id); err != nil {
		if errors.Is(err, rbac.ErrNotFound) {
			writeError(w, http.StatusNotFound, "assignment not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	username := a.UserID
	if u, err := h.Auth.GetUser(r.Context(), a.UserID); err == nil {
		username = u.Username
	}
	h.auditAssignment(r, audit.ActionAssignmentRemove, username, a)
	w.WriteHeader(http.StatusNoContent)
}

type accessCheckRequest struct {
	UserID string `json:"user_id"`
	// Area + Level, or Extra, name what the user would do.
	Area  string `json:"area,omitempty"`
	Level string `json:"level,omitempty"`
	Extra string `json:"extra,omitempty"`
	Host  string `json:"host,omitempty"`
	Stack string `json:"stack,omitempty"`
}

type accessCheckResponse struct {
	Allowed bool `json:"allowed"`
	// GrantedBy is the assignment that allows it.
	GrantedBy *rbac.Assignment `json:"granted_by,omitempty"`
	// Closest are the user's assignments that reach the resource but
	// don't allow the action.
	Closest []rbac.Assignment `json:"closest"`
	Needed  string            `json:"needed"`
}

// CheckAccess answers "can <user> <action> on <resource>?" with the
// assignment that allows it, or the closest ones when it doesn't.
//
//	POST /api/v1/access/check
func (h *Handlers) CheckAccess(w http.ResponseWriter, r *http.Request) {
	if !h.rolesAvailable(w) {
		return
	}
	var req accessCheckRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if _, err := h.Auth.GetUser(r.Context(), req.UserID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	perms, needed, err := rbac.ActionPerms(req.Area, req.Level, req.Extra)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hostID := req.Host
	if req.Stack != "" && h.Deployments != nil {
		if d, err := h.Deployments.Get(r.Context(), req.Stack); err == nil && d != nil && d.HostID != "" {
			hostID = d.HostID
		}
	}
	res := rbac.Resource{HostID: hostID, HostTags: h.hostTags(hostID), StackName: req.Stack}
	access := h.Roles.AccessFor(req.UserID)
	out := accessCheckResponse{Allowed: true, Needed: needed, Closest: []rbac.Assignment{}}
	for _, p := range perms {
		g, ok := access.GrantFor(p, res)
		if !ok {
			out.Allowed = false
			out.GrantedBy = nil
			break
		}
		if out.GrantedBy == nil {
			a := g.Assignment
			out.GrantedBy = &a
		}
	}
	if !out.Allowed {
		for _, g := range access.Grants {
			if access.CoversGrant(g, res) {
				out.Closest = append(out.Closest, g.Assignment)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
