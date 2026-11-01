package rbac

import (
	"fmt"
	"sort"
)

// Level is one step of an area. Levels are cumulative: a role on level
// n holds the permissions of every level up to n.
type Level struct {
	Key   string   `json:"key"`
	Label string   `json:"label"`
	Items []string `json:"items"` // what the level adds, in plain words
	Perms []Perm   `json:"-"`
}

// Area groups related permissions under one level picker.
type Area struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Scoped bool    `json:"scoped"` // false: only a global assignment grants it
	Levels []Level `json:"levels"`
}

// Extra is a risky permission that no level includes.
type Extra struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Warning string `json:"warning"`
	Perms   []Perm `json:"-"`
}

const LevelNone = "none"

var none = Level{Key: LevelNone, Label: "None", Items: []string{}}

// Areas is the level catalog, in display order.
var Areas = []Area{
	{Key: "stacks", Label: "Stacks", Scoped: true, Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"See stacks, services and compose", "Read logs", "Browse templates"},
			Perms: []Perm{PermStacksView, PermContainersLogs, PermTemplatesView}},
		{Key: "operate", Label: "Operate", Items: []string{"Stop stacks", "Start, stop and restart their containers"},
			Perms: []Perm{PermStacksOperate, PermContainersUpdate}},
		{Key: "deploy", Label: "Deploy", Items: []string{"Deploy and redeploy", "Edit compose and .env", "Roll back to an earlier deploy", "Deploy from a template"},
			Perms: []Perm{PermStacksDeploy, PermStacksUpdate}},
		{Key: "manage", Label: "Manage", Items: []string{"Create and delete stacks", "Migrate to another host", "Adopt existing containers", "Connect git repositories", "Create, edit and delete templates"},
			Perms: []Perm{PermStacksCreate, PermStacksDelete, PermStacksMigrate, PermStacksAdopt, PermStacksGit, PermTemplatesCreate, PermTemplatesUpdate, PermTemplatesDelete}},
	}},
	{Key: "containers", Label: "Containers", Scoped: true, Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"See containers and their metrics", "Read logs"},
			Perms: []Perm{PermContainersView, PermContainersLogs}},
		{Key: "operate", Label: "Operate", Items: []string{"Start, stop, restart, pause"},
			Perms: []Perm{PermContainersUpdate}},
		{Key: "manage", Label: "Manage", Items: []string{"Remove containers", "Update and roll back images"},
			Perms: []Perm{PermContainersDelete, PermContainersUpgrade}},
	}},
	{Key: "resources", Label: "Images, volumes, networks & registries", Scoped: true, Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"See images, volumes and networks", "List registries"},
			Perms: []Perm{PermImagesView, PermVolumesView, PermNetworksView, PermRegistriesView}},
		{Key: "manage", Label: "Manage", Items: []string{"Pull, remove and prune", "Create volumes and networks", "Run vulnerability scans", "Add, edit and remove registries"},
			Perms: []Perm{PermImagesCreate, PermImagesDelete, PermImagesScan, PermVolumesCreate, PermVolumesDelete, PermNetworksCreate, PermNetworksDelete, PermRegistriesCreate, PermRegistriesUpdate, PermRegistriesDelete}},
	}},
	{Key: "hosts", Label: "Hosts", Scoped: true, Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"See hosts and host metrics"},
			Perms: []Perm{PermHostsView, PermMetricsView}},
		{Key: "manage", Label: "Manage", Items: []string{"Add and revoke hosts", "Edit host tags", "Drain hosts and upgrade agents"},
			Perms: []Perm{PermHostsCreate, PermHostsUpdate, PermHostsDelete, PermHostsTag}},
	}},
	{Key: "proxy", Label: "Proxy", Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"See routes and certificates"},
			Perms: []Perm{PermProxyView}},
		{Key: "manage", Label: "Manage", Items: []string{"Add, edit and delete routes", "Start and stop the proxy"},
			Perms: []Perm{PermProxyCreate, PermProxyUpdate, PermProxyDelete}},
	}},
	{Key: "backups", Label: "Backups", Scoped: true, Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"See jobs, runs and targets"},
			Perms: []Perm{PermBackupsView}},
		{Key: "operate", Label: "Operate", Items: []string{"Run jobs now", "Restore a run"},
			Perms: []Perm{PermBackupsRun, PermBackupsRestore}},
		{Key: "manage", Label: "Manage", Items: []string{"Create and edit jobs and targets"},
			Perms: []Perm{PermBackupsCreate, PermBackupsUpdate, PermBackupsDelete}},
	}},
	{Key: "alerts", Label: "Alerts", Scoped: true, Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"See rules and alert history"},
			Perms: []Perm{PermAlertsView, PermAlertsHistory}},
		{Key: "manage", Label: "Manage", Items: []string{"Edit rules and notification channels"},
			Perms: []Perm{PermAlertsCreate, PermAlertsUpdate, PermAlertsDelete}},
	}},
	{Key: "users", Label: "Users & roles", Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"See users, roles and assignments"},
			Perms: []Perm{PermUsersView, PermRolesView}},
		{Key: "manage", Label: "Manage", Items: []string{"Invite, edit and remove users", "Edit roles and assignments", "Revoke other users' API tokens"},
			Perms: []Perm{PermUsersCreate, PermUsersUpdate, PermUsersDelete, PermRolesCreate, PermRolesUpdate, PermRolesDelete, PermTokensManageOthers}},
	}},
	{Key: "settings", Label: "Settings & audit", Levels: []Level{none,
		{Key: "view", Label: "View", Items: []string{"Read the audit log"},
			Perms: []Perm{PermAuditView}},
		{Key: "manage", Label: "Manage", Items: []string{"Change instance settings", "Configure sign-in and identity providers", "Audit retention, export and SIEM webhook", "Install Dockmesh updates"},
			Perms: []Perm{PermAuditExport, PermAuditWrite, PermSystemUpdate, PermSystemUpgrade}},
	}},
}

// Extras is the catalog of risky add-ons, in display order.
var Extras = []Extra{
	{Key: "terminal", Label: "Open a terminal in containers", Warning: "Shell access — can read every file and secret inside the container.",
		Perms: []Perm{PermContainersExec}},
	{Key: "files", Label: "Browse and read files in containers and volumes", Warning: "Can read configuration files and data, including secrets stored in files.",
		Perms: []Perm{PermContainersFiles, PermVolumesBrowse}},
	{Key: "env", Label: "See environment values (inspect, .env)", Warning: "Shows secrets passed as environment variables in clear text.",
		Perms: []Perm{PermContainersEnv}},
	{Key: "backup_download", Label: "Download backup archives", Warning: "Archives contain full volume data.",
		Perms: []Perm{PermBackupsDownload}},
	{Key: "key_export", Label: "Export the backup encryption key", Warning: "With the key, any backup can be decrypted. Normally admins only.",
		Perms: []Perm{PermBackupsKeyExport}},
}

// Levels maps an area key to a level key. Missing areas are "none".
type Levels map[string]string

func areaByKey(k string) (Area, bool) {
	for _, a := range Areas {
		if a.Key == k {
			return a, true
		}
	}
	return Area{}, false
}

func levelIndex(a Area, key string) int {
	if key == "" {
		return 0
	}
	for i, l := range a.Levels {
		if l.Key == key {
			return i
		}
	}
	return -1
}

// LevelAtLeast reports whether have is the same as or above want in area.
func LevelAtLeast(area, have, want string) bool {
	a, ok := areaByKey(area)
	if !ok {
		return false
	}
	return levelIndex(a, have) >= levelIndex(a, want) && levelIndex(a, want) >= 0
}

// TopLevel returns the highest level key of area.
func TopLevel(area string) string {
	a, _ := areaByKey(area)
	if len(a.Levels) == 0 {
		return LevelNone
	}
	return a.Levels[len(a.Levels)-1].Key
}

// ValidateLevels rejects unknown areas, levels and extras.
func ValidateLevels(levels Levels, extras []string) error {
	for k, v := range levels {
		a, ok := areaByKey(k)
		if !ok {
			return fmt.Errorf("unknown area %q", k)
		}
		if levelIndex(a, v) < 0 {
			return fmt.Errorf("unknown level %q for %s", v, k)
		}
	}
	for _, e := range extras {
		if _, ok := extraByKey(e); !ok {
			return fmt.Errorf("unknown extra %q", e)
		}
	}
	return nil
}

func extraByKey(k string) (Extra, bool) {
	for _, e := range Extras {
		if e.Key == k {
			return e, true
		}
	}
	return Extra{}, false
}

// PermsFor expands levels and extras into the permission set they grant.
func PermsFor(levels Levels, extras []string) map[Perm]bool {
	out := map[Perm]bool{}
	for _, a := range Areas {
		idx := levelIndex(a, levels[a.Key])
		for i := 1; i <= idx; i++ {
			for _, p := range a.Levels[i].Perms {
				out[p] = true
			}
		}
	}
	for _, k := range extras {
		if e, ok := extraByKey(k); ok {
			for _, p := range e.Perms {
				out[p] = true
			}
		}
	}
	return out
}

// SortedPerms returns the keys of set in a stable order.
func SortedPerms(set map[Perm]bool) []Perm {
	out := make([]Perm, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// AllPerms lists every permission any level or extra can grant, plus the
// base rights.
func AllPerms() []Perm {
	set := map[Perm]bool{}
	for _, a := range Areas {
		for _, l := range a.Levels {
			for _, p := range l.Perms {
				set[p] = true
			}
		}
	}
	for _, e := range Extras {
		for _, p := range e.Perms {
			set[p] = true
		}
	}
	for p := range basePerms {
		set[p] = true
	}
	return SortedPerms(set)
}

// RoleDef is a role definition as levels and extras.
type RoleDef struct {
	Name        string
	Display     string
	Description string
	Levels      Levels
	Extras      []string
}

func allTop() Levels {
	out := Levels{}
	for _, a := range Areas {
		out[a.Key] = TopLevel(a.Key)
	}
	return out
}

// BuiltinRoles are seeded on every start and can't be edited.
var BuiltinRoles = []RoleDef{
	{Name: RoleAdmin, Display: "Admin", Description: "Everything, including users, roles, settings and updates.",
		Levels: allTop(), Extras: []string{"terminal", "files", "env", "backup_download", "key_export"}},
	{Name: RoleHostAdmin, Display: "Host Admin", Description: "Runs hosts end to end: stacks, containers, proxy, backups, alerts. No users or instance settings.",
		Levels: Levels{"stacks": "manage", "containers": "manage", "resources": "manage", "hosts": "manage", "proxy": "manage", "backups": "manage", "alerts": "manage", "settings": "view"},
		Extras: []string{"terminal", "files", "env", "backup_download"}},
	{Name: RoleDeployer, Display: "Deployer", Description: "Ships changes: deploys stacks and edits compose. Cannot delete stacks or open terminals.",
		Levels: Levels{"stacks": "deploy", "containers": "operate", "resources": "view", "hosts": "view", "proxy": "view", "backups": "view", "alerts": "view"}},
	{Name: RoleOperator, Display: "Operator", Description: "Day-to-day operations: start, stop, restart, logs and terminal. No deploys.",
		Levels: Levels{"stacks": "operate", "containers": "operate", "resources": "view", "hosts": "view", "backups": "view", "alerts": "view"},
		Extras: []string{"terminal"}},
	{Name: RoleViewer, Display: "Viewer", Description: "Read-only: sees everything it is assigned to, changes nothing.",
		Levels: Levels{"stacks": "view", "containers": "view", "resources": "view", "hosts": "view", "proxy": "view", "backups": "view", "alerts": "view", "settings": "view"}},
}

// BuiltinRole returns the built-in definition of name.
func BuiltinRole(name string) (RoleDef, bool) {
	for _, r := range BuiltinRoles {
		if r.Name == name {
			return r, true
		}
	}
	return RoleDef{}, false
}

// ActionPerms returns the permissions an action needs, for the access
// check: the permissions a level adds in an area, or an extra's.
func ActionPerms(area, level, extra string) ([]Perm, string, error) {
	if extra != "" {
		e, ok := extraByKey(extra)
		if !ok {
			return nil, "", fmt.Errorf("unknown extra %q", extra)
		}
		return e.Perms, "the extra “" + e.Label + "”", nil
	}
	a, ok := areaByKey(area)
	if !ok {
		return nil, "", fmt.Errorf("unknown area %q", area)
	}
	i := levelIndex(a, level)
	if i <= 0 {
		return nil, "", fmt.Errorf("unknown level %q for %s", level, area)
	}
	return a.Levels[i].Perms, a.Label + ": " + a.Levels[i].Label + " or higher", nil
}
