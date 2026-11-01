// Package rbac defines the access model used across the API.
//
// Endpoints are gated on fine-grained permissions (Perm). Roles do not
// list permissions directly: a role picks a level per area (stacks,
// containers, …) plus a few explicit extras, and the catalog in this
// file turns that into a permission set. Users get access through
// assignments of a role on a scope (everything, a host, a host tag or a
// stack); see Access for how assignments are evaluated.
package rbac

type Perm string

const (
	PermContainersView    Perm = "containers.view"
	PermContainersUpdate  Perm = "containers.update" // start/stop/restart/pause/kill
	PermContainersDelete  Perm = "containers.delete"
	PermContainersUpgrade Perm = "containers.upgrade" // update / roll back the image of a container
	PermContainersExec    Perm = "containers.exec"    // terminal, writing container files
	PermContainersFiles   Perm = "containers.files"   // browse and read container files
	PermContainersEnv     Perm = "containers.env"     // environment values in inspect and stack .env
	PermContainersLogs    Perm = "containers.logs"

	PermStacksView    Perm = "stacks.view"
	PermStacksOperate Perm = "stacks.operate" // stop / start existing services
	PermStacksDeploy  Perm = "stacks.deploy"
	PermStacksUpdate  Perm = "stacks.update" // edit compose.yaml and .env
	PermStacksCreate  Perm = "stacks.create"
	PermStacksDelete  Perm = "stacks.delete"
	PermStacksMigrate Perm = "stacks.migrate"
	PermStacksAdopt   Perm = "stacks.adopt"
	PermStacksGit     Perm = "stacks.git" // connect and disconnect git repositories

	PermVolumesView   Perm = "volumes.view"
	PermVolumesCreate Perm = "volumes.create"
	PermVolumesDelete Perm = "volumes.delete"
	PermVolumesBrowse Perm = "volumes.browse"

	PermNetworksView   Perm = "networks.view"
	PermNetworksCreate Perm = "networks.create"
	PermNetworksDelete Perm = "networks.delete"

	PermImagesView   Perm = "images.view"
	PermImagesCreate Perm = "images.create" // pull
	PermImagesDelete Perm = "images.delete"
	PermImagesScan   Perm = "images.scan"

	PermRegistriesView   Perm = "registries.view"
	PermRegistriesCreate Perm = "registries.create"
	PermRegistriesUpdate Perm = "registries.update"
	PermRegistriesDelete Perm = "registries.delete"

	PermHostsView   Perm = "hosts.view"
	PermHostsCreate Perm = "hosts.create"
	PermHostsUpdate Perm = "hosts.update"
	PermHostsDelete Perm = "hosts.delete"
	PermHostsTag    Perm = "hosts.tag"

	PermUsersView   Perm = "users.view"
	PermUsersCreate Perm = "users.create"
	PermUsersUpdate Perm = "users.update"
	PermUsersDelete Perm = "users.delete"

	PermRolesView   Perm = "roles.view"
	PermRolesCreate Perm = "roles.create"
	PermRolesUpdate Perm = "roles.update"
	PermRolesDelete Perm = "roles.delete"

	PermTokensView         Perm = "tokens.view"
	PermTokensCreate       Perm = "tokens.create"
	PermTokensDelete       Perm = "tokens.delete"
	PermTokensManageOthers Perm = "tokens.manage_others"

	PermBackupsView      Perm = "backups.view"
	PermBackupsRun       Perm = "backups.run"
	PermBackupsRestore   Perm = "backups.restore"
	PermBackupsCreate    Perm = "backups.create"
	PermBackupsUpdate    Perm = "backups.update"
	PermBackupsDelete    Perm = "backups.delete"
	PermBackupsDownload  Perm = "backups.download"
	PermBackupsKeyExport Perm = "backups.key_export"

	PermProxyView   Perm = "proxy.view"
	PermProxyCreate Perm = "proxy.create"
	PermProxyUpdate Perm = "proxy.update"
	PermProxyDelete Perm = "proxy.delete"

	PermAlertsView    Perm = "alerts.view" // rules and notification channels
	PermAlertsHistory Perm = "alerts.history"
	PermAlertsCreate  Perm = "alerts.create"
	PermAlertsUpdate  Perm = "alerts.update"
	PermAlertsDelete  Perm = "alerts.delete"

	PermTemplatesView   Perm = "templates.view"
	PermTemplatesCreate Perm = "templates.create"
	PermTemplatesUpdate Perm = "templates.update"
	PermTemplatesDelete Perm = "templates.delete"

	PermAuditView   Perm = "audit.view"
	PermAuditExport Perm = "audit.export"
	PermAuditWrite  Perm = "audit.write"

	PermSystemView    Perm = "system.view"
	PermSystemUpdate  Perm = "system.update"
	PermSystemUpgrade Perm = "system.upgrade"

	PermMetricsView Perm = "metrics.view"
)

// Built-in role names.
const (
	RoleAdmin     = "admin"
	RoleHostAdmin = "host-admin"
	RoleDeployer  = "deployer"
	RoleOperator  = "operator"
	RoleViewer    = "viewer"
)

// basePerms belong to every signed-in user regardless of assignments:
// their own API tokens, health and version information.
var basePerms = map[Perm]bool{
	PermTokensView:   true,
	PermTokensCreate: true,
	PermTokensDelete: true,
	PermSystemView:   true,
}

// IsBase reports whether p is a base right of every signed-in user.
func IsBase(p Perm) bool { return basePerms[p] }

// globalOnly permissions act on instance-wide objects (proxy, alert rules,
// users and roles, settings, audit) or would reach beyond a scope (tagging
// hosts, enrolling new ones, the backup key). Only an assignment on
// everything grants them.
var globalOnly = map[Perm]bool{
	PermProxyView: true, PermProxyCreate: true, PermProxyUpdate: true, PermProxyDelete: true,
	PermAlertsView: true, PermAlertsCreate: true, PermAlertsUpdate: true, PermAlertsDelete: true,
	PermUsersView: true, PermUsersCreate: true, PermUsersUpdate: true, PermUsersDelete: true,
	PermRolesView: true, PermRolesCreate: true, PermRolesUpdate: true, PermRolesDelete: true,
	PermTokensManageOthers: true,
	PermAuditView:          true, PermAuditExport: true, PermAuditWrite: true,
	PermSystemUpdate: true, PermSystemUpgrade: true,
	PermHostsCreate: true, PermHostsTag: true,
	PermRegistriesCreate: true, PermRegistriesUpdate: true, PermRegistriesDelete: true,
	PermTemplatesCreate: true, PermTemplatesUpdate: true, PermTemplatesDelete: true,
	PermMetricsView:      true,
	PermBackupsKeyExport: true,
}

// IsGlobalOnly reports whether p can only be granted by a global assignment.
func IsGlobalOnly(p Perm) bool { return globalOnly[p] }
