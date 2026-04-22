// Package rbac defines the permission model used across the API.
//
// Phase 2 RBAC is role-based only (no per-resource scopes yet). Each role
// maps to a fixed set of permissions and the middleware checks against that
// map. Scope-based permissions (per-stack, per-host) come in Phase 2.3 when
// we add the scopes table to the DB.
package rbac

type Perm string

const (
	// Read permissions
	PermRead Perm = "read" // list/inspect everything non-sensitive

	// Container lifecycle
	PermContainerControl Perm = "container.control" // start/stop/restart/remove
	PermContainerExec    Perm = "container.exec"    // shell into container

	// Stack lifecycle
	PermStackWrite  Perm = "stack.write"  // create/update/delete compose files
	PermStackDeploy Perm = "stack.deploy" // deploy/stop stacks
	PermStackAdopt  Perm = "stack.adopt"  // take over a running compose project started outside dockmesh

	// Image / network / volume management
	PermImageWrite   Perm = "image.write"
	PermImageScan    Perm = "image.scan"
	PermNetworkWrite Perm = "network.write"
	PermVolumeWrite  Perm = "volume.write"

	// User management (admin domain)
	PermUserManage Perm = "user.manage"

	// Audit access (admin domain)
	PermAuditRead Perm = "audit.read"

	// Prometheus /metrics scraping. Separate from audit.read so a
	// scraping API token can be narrowly scoped to just metrics.
	PermMetricsRead Perm = "metrics.read"
)

// Role is one of "admin" | "operator" | "viewer".
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// rolePerms maps each role to its granted permissions.
//
// Concept §2.3:
//   Admin:    full access
//   Operator: container start/stop/restart, stack deploy/stop, logs, exec
//   Viewer:   read-only dashboard
var rolePerms = map[Role]map[Perm]bool{
	RoleAdmin: {
		PermRead:             true,
		PermContainerControl: true,
		PermContainerExec:    true,
		PermStackWrite:       true,
		PermStackDeploy:      true,
		PermStackAdopt:       true,
		PermImageWrite:       true,
		PermImageScan:        true,
		PermNetworkWrite:     true,
		PermVolumeWrite:      true,
		PermUserManage:       true,
		PermAuditRead:        true,
		PermMetricsRead:      true,
	},
	RoleOperator: {
		PermRead:             true,
		PermContainerControl: true,
		PermContainerExec:    true,
		PermStackDeploy:      true,
		PermImageScan:        true,
		PermAuditRead:        true,
		// Deliberately omitted: PermStackWrite (editing compose files),
		// PermImageWrite, PermNetworkWrite, PermVolumeWrite, PermUserManage
	},
	RoleViewer: {
		PermRead: true,
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
