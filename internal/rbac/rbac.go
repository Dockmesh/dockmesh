// Package rbac defines the permission model used across the API.
//
// RBAC v2 (v0.3.0) — see project_rbac_v2_spec.md memory for the full
// design. Permissions follow `category.verb` naming. ~50 permissions
// across 17 categories. Built-in roles: viewer / operator / deployer /
// host-admin / superadmin (5-role tier). Scoping at role level (not
// user level) supports per-host + per-stack granularity.
package rbac

type Perm string

const (
	// ─── Containers ───
	// containers.create is intentionally NOT defined: Dockmesh is
	// stacks-first today, no POST /containers endpoint. When we add
	// standalone-container-create the perm slots in here without a
	// matrix UI restructure. See project_standalone_container_create.md.
	PermContainersView   Perm = "containers.view"
	PermContainersUpdate Perm = "containers.update" // start/stop/restart/pause/kill (lifecycle on existing)
	PermContainersDelete Perm = "containers.delete"
	PermContainersExec   Perm = "containers.exec" // shell-in (sensitive)
	PermContainersLogs   Perm = "containers.logs" // stream logs (sensitive — PII risk)

	// ─── Stacks ───
	PermStacksView    Perm = "stacks.view"
	PermStacksCreate  Perm = "stacks.create"  // new compose.yaml on disk
	PermStacksUpdate  Perm = "stacks.update"  // edit existing compose.yaml
	PermStacksDelete  Perm = "stacks.delete"
	PermStacksDeploy  Perm = "stacks.deploy"  // deploy / stop / scale (sensitive — runs containers)
	PermStacksMigrate Perm = "stacks.migrate" // host-to-host move (sensitive — data movement)
	PermStacksAdopt   Perm = "stacks.adopt"   // adopt external compose projects

	// ─── Volumes ───
	// Docker volumes are immutable once created — no "update" verb.
	PermVolumesView     Perm = "volumes.view"
	PermVolumesCreate   Perm = "volumes.create"
	PermVolumesDelete   Perm = "volumes.delete" // remove + prune
	PermVolumesBrowse   Perm = "volumes.browse"    // file browser (sensitive — PII)
	PermVolumesReadFile Perm = "volumes.read_file" // individual file content

	// ─── Networks ───
	// Docker networks are immutable once created — no "update" verb.
	PermNetworksView   Perm = "networks.view"
	PermNetworksCreate Perm = "networks.create"
	PermNetworksDelete Perm = "networks.delete" // remove + prune

	// ─── Images ───
	// Images are immutable; "update" of an image is just pull-then-replace.
	PermImagesView   Perm = "images.view"
	PermImagesCreate Perm = "images.create" // pull (= create local copy of remote image)
	PermImagesDelete Perm = "images.delete" // remove + prune
	PermImagesScan   Perm = "images.scan"

	// ─── Registries ───
	PermRegistriesView   Perm = "registries.view"
	PermRegistriesCreate Perm = "registries.create"
	PermRegistriesUpdate Perm = "registries.update" // edit existing creds
	PermRegistriesDelete Perm = "registries.delete"

	// ─── Hosts ───
	PermHostsView   Perm = "hosts.view"
	PermHostsCreate Perm = "hosts.create" // enroll a new agent / register a host
	PermHostsUpdate Perm = "hosts.update" // drain / upgrade existing
	PermHostsDelete Perm = "hosts.delete" // revoke / remove
	PermHostsTag    Perm = "hosts.tag"

	// ─── Users (admin-only domain) ───
	PermUsersView          Perm = "users.view"
	PermUsersCreate        Perm = "users.create"
	PermUsersUpdate        Perm = "users.update" // edit role / scope / email
	PermUsersDelete        Perm = "users.delete"
	PermUsersPasswordReset Perm = "users.password_reset"
	PermUsersSuspend       Perm = "users.suspend"

	// ─── Roles (admin-only domain) ───
	PermRolesView   Perm = "roles.view"
	PermRolesCreate Perm = "roles.create"
	PermRolesUpdate Perm = "roles.update"
	PermRolesDelete Perm = "roles.delete"

	// ─── API tokens ───
	PermTokensView          Perm = "tokens.view"          // own
	PermTokensCreate        Perm = "tokens.create"        // create own
	PermTokensDelete        Perm = "tokens.delete"        // revoke own
	PermTokensManageOthers  Perm = "tokens.manage_others" // admin override

	// ─── Backups ───
	PermBackupsView    Perm = "backups.view"
	PermBackupsCreate  Perm = "backups.create"  // create new jobs / targets
	PermBackupsUpdate  Perm = "backups.update"  // edit existing jobs / targets
	PermBackupsDelete  Perm = "backups.delete"
	PermBackupsRestore Perm = "backups.restore" // sensitive — overwrites data

	// ─── Reverse proxy ───
	PermProxyView   Perm = "proxy.view"
	PermProxyCreate Perm = "proxy.create"
	PermProxyUpdate Perm = "proxy.update"
	PermProxyDelete Perm = "proxy.delete"

	// ─── Notification channels + alert rules ───
	PermAlertsView   Perm = "alerts.view"
	PermAlertsCreate Perm = "alerts.create"
	PermAlertsUpdate Perm = "alerts.update"
	PermAlertsDelete Perm = "alerts.delete"

	// ─── Stack templates ───
	PermTemplatesView   Perm = "templates.view"
	PermTemplatesCreate Perm = "templates.create"
	PermTemplatesUpdate Perm = "templates.update"
	PermTemplatesDelete Perm = "templates.delete"

	// ─── Audit log ───
	PermAuditView   Perm = "audit.view"
	PermAuditExport Perm = "audit.export" // sensitive — exfiltration
	PermAuditWrite  Perm = "audit.write"  // configure webhook + retention (gates LOOPHOLE today)

	// ─── System ───
	PermSystemView   Perm = "system.view"
	PermSystemUpdate Perm = "system.update" // settings + global env (rename: not the binary self-update)
	PermSystemUpgrade Perm = "system.upgrade" // self-update server binary (sensitive — most destructive)

	// ─── Metrics ───
	PermMetricsView Perm = "metrics.view"
)

// Role is one of the five built-in roles (RBAC v2):
//   admin / host-admin / deployer / operator / viewer
type Role string

const (
	RoleAdmin     Role = "admin"
	RoleHostAdmin Role = "host-admin"
	RoleDeployer  Role = "deployer"
	RoleOperator  Role = "operator"
	RoleViewer    Role = "viewer"
)

// rolePerms is the in-memory fallback used when the DB-backed roles
// store is unavailable (e.g., very early startup or test setups). The
// authoritative source is the `roles` + `role_permissions` tables —
// see migration 040_rbac_v21_explicit_create.sql for the seeded grants.
//
// RBAC v2.1 (explicit create): each resource has its own *.create verb
// separate from *.update. Built-in roles grant create + deploy together
// where the user-mental-model expects them as a pair (deployer/host-admin/
// admin). operator gets stacks.deploy WITHOUT stacks.create — so it can
// re-deploy existing stacks but not write new compose files. The 5-role
// ladder mirrors the user-facing tier: viewer < operator < deployer <
// host-admin < admin.
var rolePerms = map[Role]map[Perm]bool{
	RoleAdmin: {
		PermContainersView: true, PermContainersUpdate: true, PermContainersDelete: true,
		PermContainersExec: true, PermContainersLogs: true,
		PermStacksView: true, PermStacksCreate: true, PermStacksUpdate: true,
		PermStacksDelete: true, PermStacksDeploy: true,
		PermStacksMigrate: true, PermStacksAdopt: true,
		PermVolumesView: true, PermVolumesCreate: true, PermVolumesDelete: true,
		PermVolumesBrowse: true, PermVolumesReadFile: true,
		PermNetworksView: true, PermNetworksCreate: true, PermNetworksDelete: true,
		PermImagesView: true, PermImagesCreate: true, PermImagesDelete: true, PermImagesScan: true,
		PermRegistriesView: true, PermRegistriesCreate: true, PermRegistriesUpdate: true, PermRegistriesDelete: true,
		PermHostsView: true, PermHostsCreate: true, PermHostsUpdate: true, PermHostsDelete: true, PermHostsTag: true,
		PermUsersView: true, PermUsersCreate: true, PermUsersUpdate: true, PermUsersDelete: true,
		PermUsersPasswordReset: true, PermUsersSuspend: true,
		PermRolesView: true, PermRolesCreate: true, PermRolesUpdate: true, PermRolesDelete: true,
		PermTokensView: true, PermTokensCreate: true, PermTokensDelete: true, PermTokensManageOthers: true,
		PermBackupsView: true, PermBackupsCreate: true, PermBackupsUpdate: true, PermBackupsDelete: true, PermBackupsRestore: true,
		PermProxyView: true, PermProxyCreate: true, PermProxyUpdate: true, PermProxyDelete: true,
		PermAlertsView: true, PermAlertsCreate: true, PermAlertsUpdate: true, PermAlertsDelete: true,
		PermTemplatesView: true, PermTemplatesCreate: true, PermTemplatesUpdate: true, PermTemplatesDelete: true,
		PermAuditView: true, PermAuditExport: true, PermAuditWrite: true,
		PermSystemView: true, PermSystemUpdate: true, PermSystemUpgrade: true,
		PermMetricsView: true,
	},
	RoleHostAdmin: {
		// Full control of assigned hosts; everything except user/role/
		// audit-config/system-upgrade.
		PermContainersView: true, PermContainersUpdate: true, PermContainersDelete: true,
		PermContainersExec: true, PermContainersLogs: true,
		PermStacksView: true, PermStacksCreate: true, PermStacksUpdate: true,
		PermStacksDelete: true, PermStacksDeploy: true,
		PermStacksMigrate: true, PermStacksAdopt: true,
		PermVolumesView: true, PermVolumesCreate: true, PermVolumesDelete: true,
		PermVolumesBrowse: true, PermVolumesReadFile: true,
		PermNetworksView: true, PermNetworksCreate: true, PermNetworksDelete: true,
		PermImagesView: true, PermImagesCreate: true, PermImagesDelete: true, PermImagesScan: true,
		PermRegistriesView: true, PermRegistriesCreate: true, PermRegistriesUpdate: true, PermRegistriesDelete: true,
		PermHostsView: true, PermHostsCreate: true, PermHostsUpdate: true, PermHostsDelete: true, PermHostsTag: true,
		PermUsersView: true, PermRolesView: true,
		PermTokensView: true, PermTokensCreate: true, PermTokensDelete: true,
		PermBackupsView: true, PermBackupsCreate: true, PermBackupsUpdate: true, PermBackupsDelete: true, PermBackupsRestore: true,
		PermProxyView: true, PermProxyCreate: true, PermProxyUpdate: true, PermProxyDelete: true,
		PermAlertsView: true, PermAlertsCreate: true, PermAlertsUpdate: true, PermAlertsDelete: true,
		PermTemplatesView: true, PermTemplatesCreate: true, PermTemplatesUpdate: true, PermTemplatesDelete: true,
		PermAuditView: true, PermAuditExport: true,
		PermSystemView: true, PermSystemUpdate: true,
		PermMetricsView: true,
	},
	RoleDeployer: {
		// Operator + create/update/delete on stacks + create on
		// images/volumes/networks. The "deploy author" tier.
		PermContainersView: true, PermContainersUpdate: true, PermContainersDelete: true,
		PermContainersExec: true, PermContainersLogs: true,
		PermStacksView: true, PermStacksCreate: true, PermStacksUpdate: true,
		PermStacksDelete: true, PermStacksDeploy: true, PermStacksAdopt: true,
		PermVolumesView: true, PermVolumesCreate: true,
		PermNetworksView: true, PermNetworksCreate: true,
		PermImagesView: true, PermImagesCreate: true, PermImagesScan: true,
		PermRegistriesView: true, PermHostsView: true,
		PermUsersView: true, PermRolesView: true,
		PermTokensView: true, PermTokensCreate: true, PermTokensDelete: true,
		PermBackupsView: true,
		PermProxyView: true, PermAlertsView: true,
		PermTemplatesView: true, PermTemplatesCreate: true, PermTemplatesUpdate: true,
		PermAuditView: true,
		PermSystemView: true, PermMetricsView: true,
	},
	RoleOperator: {
		// Lifecycle on existing resources. NO create — operator can re-
		// deploy existing stacks but NOT write new compose files.
		PermContainersView: true, PermContainersUpdate: true,
		PermContainersExec: true, PermContainersLogs: true,
		PermStacksView: true, PermStacksDeploy: true,
		PermVolumesView: true, PermNetworksView: true,
		PermImagesView: true, PermImagesScan: true,
		PermRegistriesView: true, PermHostsView: true,
		PermUsersView: true, PermRolesView: true,
		PermTokensView: true, PermTokensCreate: true, PermTokensDelete: true,
		PermBackupsView: true, PermProxyView: true, PermAlertsView: true,
		PermTemplatesView: true, PermAuditView: true,
		PermSystemView: true, PermMetricsView: true,
	},
	RoleViewer: {
		// Every *.view perm.
		PermContainersView: true, PermStacksView: true, PermVolumesView: true,
		PermNetworksView: true, PermImagesView: true, PermRegistriesView: true,
		PermHostsView: true, PermUsersView: true, PermRolesView: true,
		PermTokensView: true, PermBackupsView: true, PermProxyView: true,
		PermAlertsView: true, PermTemplatesView: true, PermAuditView: true,
		PermSystemView: true, PermMetricsView: true,
	},
}

// Allowed reports whether the given role is granted the permission.
// Unknown roles have no permissions.
func Allowed(role string, perm Perm) bool {
	perms, ok := rolePerms[Role(role)]
	if !ok {
		return false
	}
	return perms[perm]
}

// RolePerms returns the set of permissions for a role (for the UI to gate
// buttons). Nil if the role is unknown.
func RolePerms(role string) []Perm {
	perms, ok := rolePerms[Role(role)]
	if !ok {
		return nil
	}
	out := make([]Perm, 0, len(perms))
	for p := range perms {
		out = append(out, p)
	}
	return out
}
