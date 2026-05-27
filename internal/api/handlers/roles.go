package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// ListRoles returns all roles (built-in + custom) with permissions.
//
//	GET /api/v1/roles
func (h *Handlers) ListRoles(w http.ResponseWriter, r *http.Request) {
	if h.Roles == nil {
		// Fallback: return hardcoded roles.
		writeJSON(w, http.StatusOK, []rbac.CustomRole{
			{Name: "admin", Display: "Admin", Description: "Full control of the platform: users, roles, audit config, system updates. The only role that can manage other admins.", Builtin: true, Permissions: rbac.RolePerms("admin")},
			{Name: "host-admin", Display: "Host Admin", Description: "Full control of assigned hosts: manage proxy, backups, alerts, registries. No user, role, or audit-config access.", Builtin: true, Permissions: rbac.RolePerms("host-admin")},
			{Name: "deployer", Display: "Deployer", Description: "Operate stacks and ship code: deploy + edit compose + pull images. No host or user management.", Builtin: true, Permissions: rbac.RolePerms("deployer")},
			{Name: "operator", Display: "Operator", Description: "Day-to-day operations: start/stop containers, view logs and exec into them. No deploy, no destroy.", Builtin: true, Permissions: rbac.RolePerms("operator")},
			{Name: "viewer", Display: "Viewer", Description: "Read-only across the fleet. View dashboards, logs, audit log, metrics — no mutations.", Builtin: true, Permissions: rbac.RolePerms("viewer")},
		})
		return
	}
	writeJSON(w, http.StatusOK, h.Roles.List())
}

// GetRole returns a single role by name.
//
//	GET /api/v1/roles/{name}
func (h *Handlers) GetRole(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if h.Roles == nil {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	role, ok := h.Roles.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	writeJSON(w, http.StatusOK, role)
}

// CreateRole creates a new custom role.
//
//	POST /api/v1/roles
func (h *Handlers) CreateRole(w http.ResponseWriter, r *http.Request) {
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return
	}
	var in rbac.RoleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.Name == "" || in.Display == "" {
		writeError(w, http.StatusBadRequest, "name and display required")
		return
	}
	if err := h.Roles.Create(r.Context(), in); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.audit(r, audit.ActionUserCreate, in.Name, map[string]any{
		"action":      "role-create",
		"permissions": len(in.Permissions),
	})
	role, _ := h.Roles.Get(in.Name)
	writeJSON(w, http.StatusCreated, role)
}

// UpdateRole modifies a custom role (built-in roles cannot be edited).
//
//	PUT /api/v1/roles/{name}
func (h *Handlers) UpdateRole(w http.ResponseWriter, r *http.Request) {
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	var in rbac.RoleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.Roles.Update(r.Context(), name, in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionUserCreate, name, map[string]any{
		"action":      "role-update",
		"permissions": len(in.Permissions),
	})
	role, _ := h.Roles.Get(name)
	writeJSON(w, http.StatusOK, role)
}

// DeleteRole removes a custom role (built-in roles cannot be deleted).
//
//	DELETE /api/v1/roles/{name}
func (h *Handlers) DeleteRole(w http.ResponseWriter, r *http.Request) {
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return
	}
	name := chi.URLParam(r, "name")
	if err := h.Roles.Delete(r.Context(), name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionUserCreate, name, map[string]any{"action": "role-delete"})
	w.WriteHeader(http.StatusNoContent)
}

// AllPermissions returns the full Dockmesh RBAC v2 permission catalog
// — see project_rbac_v2_spec.md memory for the design. Each entry has:
//
//   - name         "category.verb" dot-notation backend identifier
//   - display_name Human-readable verb label (UI matrix cell title)
//   - description  Plain-language explanation (? tooltip text)
//   - category     UI matrix grouping ("Containers", "Stacks", ...)
//   - verb         matrix column header within the category — view /
//                  deploy / update / delete / + per-resource sensitive
//                  verbs (exec / logs / browse / migrate / scan ...)
//   - danger_level "low" | "medium" | "high" — drives UI tinting on
//                  dangerous grants
//   - sensitive    true if the permission belongs in the "Sensitive
//                  operations" sub-section of its category (exec / logs
//                  / browse / migrate / etc.) rather than the standard
//                  view / deploy / update / delete row
//
// Order in this slice = display order in the UI matrix.
//
//	GET /api/v1/roles/permissions
func (h *Handlers) AllPermissions(w http.ResponseWriter, r *http.Request) {
	type permInfo struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		Category    string `json:"category"`
		Verb        string `json:"verb"`
		DangerLevel string `json:"danger_level"`
		Sensitive   bool   `json:"sensitive,omitempty"`
	}
	perms := []permInfo{
		// ── Containers ─────────────────────────────────────────────────
		// containers.create is intentionally absent: stacks-first design
		// today (no POST /containers endpoint). UI matrix renders `—` in
		// the create column for containers. See memory.
		{"containers.view", "View containers", "List containers, inspect their config, see status and resource usage.", "Containers", "view", "low", false},
		{"containers.update", "Lifecycle (start / stop / restart)", "Start, stop, restart, pause, kill running containers. Operates on existing containers — does NOT create new ones (containers come from stack deploys).", "Containers", "update", "medium", false},
		{"containers.delete", "Remove containers", "Delete containers (data inside the container is lost; volumes are preserved).", "Containers", "delete", "medium", false},
		{"containers.exec", "Shell into container", "Open an interactive terminal inside a running container — equivalent to SSH access on that container.", "Containers", "exec", "high", true},
		{"containers.logs", "Stream container logs", "Read live + historical logs. Sensitive because logs may contain secrets, PII, or customer data.", "Containers", "logs", "medium", true},

		// ── Stacks ─────────────────────────────────────────────────────
		{"stacks.view", "View stacks", "List stacks, inspect their compose files, see deploy history and status.", "Stacks", "view", "low", false},
		{"stacks.create", "Create new stack", "Write a new compose.yaml on disk. By itself does not run anything — pair with stacks.deploy to actually start containers.", "Stacks", "create", "medium", false},
		{"stacks.update", "Edit existing stack", "Modify an existing stack's compose.yaml on disk. Like create, this is file-only — deploy is separate.", "Stacks", "update", "medium", false},
		{"stacks.delete", "Delete stacks", "Remove a stack and its compose files. Volumes are preserved unless explicitly purged.", "Stacks", "delete", "high", false},
		{"stacks.deploy", "Deploy & redeploy", "Deploy, stop, scale, redeploy stacks. Runs compose-up on the target host. Operator-tier roles get this WITHOUT create — they can re-run existing stacks but not author new ones.", "Stacks", "deploy", "medium", true},
		{"stacks.migrate", "Migrate to another host", "Move a stack between hosts: stop on source, transfer volumes, start on target. Includes data movement.", "Stacks", "migrate", "high", true},
		{"stacks.adopt", "Adopt external stack", "Take over a compose project that was started outside Dockmesh — so it's managed from here on.", "Stacks", "adopt", "low", true},

		// ── Volumes ────────────────────────────────────────────────────
		// Docker volumes are immutable once created — no update verb.
		{"volumes.view", "View volumes", "List volumes, see size, mount points, and which containers use them.", "Volumes", "view", "low", false},
		{"volumes.create", "Create volumes", "Create new Docker volumes (named, with driver and labels).", "Volumes", "create", "medium", false},
		{"volumes.delete", "Remove volumes", "Remove individual volumes or bulk-prune unused ones. Loss of a volume can mean loss of stateful data.", "Volumes", "delete", "high", false},
		{"volumes.browse", "Browse volume files", "Open a file-tree browser into the volume's content. Sensitive — volumes often contain plaintext secrets, customer data, database files.", "Volumes", "browse", "high", true},
		{"volumes.read_file", "Read volume file contents", "Open and read individual files inside a volume. Most sensitive read access in the system.", "Volumes", "read_file", "high", true},

		// ── Networks ───────────────────────────────────────────────────
		// Docker networks are immutable once created — no update verb.
		{"networks.view", "View networks", "List Docker networks, see their drivers, subnets, attached containers.", "Networks", "view", "low", false},
		{"networks.create", "Create networks", "Create custom Docker networks (bridge, overlay, etc.).", "Networks", "create", "medium", false},
		{"networks.delete", "Remove networks", "Remove individual networks or bulk-prune unused ones.", "Networks", "delete", "medium", false},

		// ── Images ─────────────────────────────────────────────────────
		// Images are immutable; "update" of an image is just pull-then-replace.
		{"images.view", "View images", "List images on the host, see size, layers, and which containers use them.", "Images", "view", "low", false},
		{"images.create", "Pull images", "Pull (download) images from registries. Creates a local copy of a remote image.", "Images", "create", "medium", false},
		{"images.delete", "Remove images", "Remove individual images or bulk-prune unused ones. Reclaims disk space.", "Images", "delete", "low", false},
		{"images.scan", "Vulnerability scan", "Run a Grype vulnerability scan on an image. Only reads the image, doesn't modify it.", "Images", "scan", "low", true},

		// ── Registries ─────────────────────────────────────────────────
		{"registries.view", "View registries", "List configured private registries (no credential read access).", "Registries", "view", "low", false},
		{"registries.create", "Add registry", "Add a new private-registry credential entry.", "Registries", "create", "medium", false},
		{"registries.update", "Edit registry", "Edit an existing private-registry credential.", "Registries", "update", "medium", false},
		{"registries.delete", "Remove registries", "Disconnect a private registry. Containers using its images keep working until next pull.", "Registries", "delete", "low", false},

		// ── Hosts ──────────────────────────────────────────────────────
		{"hosts.view", "View hosts", "List managed hosts (local + remote agents), see their status, resources, version.", "Hosts", "view", "low", false},
		{"hosts.create", "Enroll host", "Enroll a new remote host: generate the install token and register it in the fleet.", "Hosts", "create", "medium", false},
		{"hosts.update", "Drain & upgrade hosts", "Drain a host (move stacks to other hosts), upgrade the agent binary on existing hosts.", "Hosts", "update", "medium", false},
		{"hosts.delete", "Revoke hosts", "Revoke a host's certificate and remove it from the fleet. The remote machine stays as-is.", "Hosts", "delete", "medium", false},
		{"hosts.tag", "Manage host tags", "Edit tags on hosts. Tags drive scope filtering for users and roles.", "Hosts", "tag", "low", true},

		// ── Users ──────────────────────────────────────────────────────
		{"users.view", "View users", "List users and their assigned roles. No password / token access.", "Users", "view", "low", false},
		{"users.create", "Add user", "Create a new user account with role and initial password / invitation.", "Users", "create", "high", false},
		{"users.update", "Edit user", "Edit a user's email, role, and scope assignment.", "Users", "update", "high", false},
		{"users.delete", "Remove users", "Delete user accounts. Their API tokens are revoked. Active sessions log out on next refresh.", "Users", "delete", "high", false},
		{"users.password_reset", "Reset passwords", "Set a new password for any user. Used for forgotten-password recovery.", "Users", "password_reset", "high", true},
		{"users.suspend", "Suspend users", "Disable a user's login without deleting them. Sessions terminate immediately.", "Users", "suspend", "medium", true},

		// ── Roles ──────────────────────────────────────────────────────
		{"roles.view", "View roles", "List roles and their permission grants.", "Roles", "view", "low", false},
		{"roles.create", "Create role", "Add a new custom role with its initial permissions and scope.", "Roles", "create", "high", false},
		{"roles.update", "Edit role", "Edit an existing custom role's permissions, scope, or description.", "Roles", "update", "high", false},
		{"roles.delete", "Delete roles", "Remove a custom role. Users with that role lose its permissions immediately.", "Roles", "delete", "high", false},

		// ── API tokens ─────────────────────────────────────────────────
		{"tokens.view", "View own tokens", "List the API tokens you yourself created.", "API tokens", "view", "low", false},
		{"tokens.create", "Create own tokens", "Create new API tokens scoped to your role + scope.", "API tokens", "create", "medium", false},
		{"tokens.delete", "Revoke own tokens", "Revoke tokens you yourself created.", "API tokens", "delete", "low", false},
		{"tokens.manage_others", "Manage all tokens (admin)", "View and revoke API tokens created by any user. For incident response.", "API tokens", "manage_others", "high", true},

		// ── Backups ────────────────────────────────────────────────────
		{"backups.view", "View backup status", "List backup targets, jobs, and recent runs.", "Backups", "view", "low", false},
		{"backups.create", "Create backup config", "Add new backup targets or jobs. Trigger manual runs of new jobs.", "Backups", "create", "medium", false},
		{"backups.update", "Edit backup config", "Edit existing backup targets or jobs.", "Backups", "update", "medium", false},
		{"backups.delete", "Remove backup configs", "Delete backup targets and jobs (does not delete backups already on the target).", "Backups", "delete", "medium", false},
		{"backups.restore", "Restore from backup", "Restore a stack or volume from a backup run. Overwrites current data.", "Backups", "restore", "high", true},

		// ── Reverse proxy ──────────────────────────────────────────────
		{"proxy.view", "View proxy routes", "List Caddy reverse-proxy routes and their TLS modes.", "Reverse proxy", "view", "low", false},
		{"proxy.create", "Add proxy route", "Add a new reverse-proxy route (host name, upstream, TLS mode).", "Reverse proxy", "create", "medium", false},
		{"proxy.update", "Edit proxy route", "Edit an existing reverse-proxy route.", "Reverse proxy", "update", "medium", false},
		{"proxy.delete", "Remove proxy routes", "Delete reverse-proxy routes. Affects external accessibility of services.", "Reverse proxy", "delete", "medium", false},

		// ── Alerts ─────────────────────────────────────────────────────
		{"alerts.view", "View alerts", "List notification channels and alert rules.", "Alerts", "view", "low", false},
		{"alerts.create", "Add alert", "Add new notification channels or alert rules.", "Alerts", "create", "medium", false},
		{"alerts.update", "Edit alert", "Edit existing notification channels or alert rules.", "Alerts", "update", "medium", false},
		{"alerts.delete", "Remove alerts", "Delete notification channels and alert rules.", "Alerts", "delete", "medium", false},

		// ── Templates ──────────────────────────────────────────────────
		{"templates.view", "View templates", "Browse the stack-template catalog.", "Templates", "view", "low", false},
		{"templates.create", "Create template", "Add a new stack template available to all users.", "Templates", "create", "medium", false},
		{"templates.update", "Edit template", "Edit an existing stack template.", "Templates", "update", "medium", false},
		{"templates.delete", "Remove templates", "Delete stack templates from the catalog.", "Templates", "delete", "low", false},

		// ── Audit ──────────────────────────────────────────────────────
		{"audit.view", "View audit log", "Read the audit log entries and verify the tamper-evident hash chain.", "Audit", "view", "low", false},
		{"audit.export", "Export audit log", "Download or stream the audit log to external systems. Sensitive — exfiltration risk separate from read access.", "Audit", "export", "medium", true},
		{"audit.write", "Configure audit settings", "Configure audit webhook destinations and retention policy. Without this perm, an audit reader cannot tamper with the compliance trail.", "Audit", "write", "high", true},

		// ── System ─────────────────────────────────────────────────────
		{"system.view", "View system settings", "View global env variables, system-wide configuration, server health.", "System", "view", "low", false},
		{"system.update", "Edit system settings", "Modify global env variables and platform settings.", "System", "update", "medium", false},
		{"system.upgrade", "Upgrade Dockmesh server", "Trigger a self-update of the Dockmesh server binary. The most destructive operation in the platform — highest risk class.", "System", "upgrade", "high", true},

		// ── Metrics ────────────────────────────────────────────────────
		{"metrics.view", "Scrape Prometheus metrics", "Read the /metrics endpoint. Mostly used by narrowly-scoped API tokens for monitoring scrapers.", "Metrics", "view", "low", false},
	}
	writeJSON(w, http.StatusOK, perms)
}
