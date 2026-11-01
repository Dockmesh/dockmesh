package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// Gates that are not a permission.
const (
	gatePublic   rbac.Perm = "public"    // no authentication
	gateSignedIn rbac.Perm = "signed-in" // any signed-in caller; the handler filters
	gateTicket   rbac.Perm = "ticket"    // WebSocket ticket for a permission
)

// routeGates is what every route of the router requires. A route added
// to router.go without an entry here fails TestRouteGatesCoverEveryRoute;
// whether the permission is scoped or global follows rbac.IsGlobalOnly.
var routeGates = []struct {
	method, path string
	gate         rbac.Perm
}{
	{"GET", "/healthz/live", gatePublic},
	{"GET", "/healthz/ready", gatePublic},
	{"GET", "/metrics", rbac.PermMetricsView},
	{"GET", "/api/v1/health", gatePublic},
	{"GET", "/api/v1/setup/status", gatePublic},
	{"GET", "/api/v1/setup/preflight", gatePublic},
	{"GET", "/api/v1/setup/server-info", gatePublic},
	{"GET", "/api/v1/setup/stream/{run_id}", gatePublic},
	{"GET", "/api/v1/openapi.json", gatePublic},
	{"GET", "/api/v1/openapi.yaml", gatePublic},
	{"GET", "/api/v1/docs", gatePublic},
	{"POST", "/api/v1/auth/login", gatePublic},
	{"POST", "/api/v1/auth/mfa", gatePublic},
	{"POST", "/api/v1/auth/logout", gatePublic},
	{"POST", "/api/v1/auth/refresh", gatePublic},
	{"POST", "/api/v1/auth/sso/exchange", gatePublic},
	{"GET", "/api/v1/auth/oidc/providers", gatePublic},
	{"GET", "/api/v1/auth/oidc/{slug}/login", gatePublic},
	{"GET", "/api/v1/auth/oidc/{slug}/callback", gatePublic},
	{"GET", "/api/v1/auth/saml/providers", gatePublic},
	{"GET", "/api/v1/auth/saml/{slug}/login", gatePublic},
	{"POST", "/api/v1/auth/saml/{slug}/acs", gatePublic},
	{"GET", "/api/v1/auth/saml/{slug}/metadata", gatePublic},
	{"GET", "/api/v1/auth/ldap/providers", gatePublic},
	{"POST", "/api/v1/auth/ldap/{slug}/login", gatePublic},
	{"GET", "/api/v1/auth/oauth2/providers", gatePublic},
	{"GET", "/api/v1/auth/oauth2/{slug}/login", gatePublic},
	{"GET", "/api/v1/auth/oauth2/{slug}/callback", gatePublic},
	{"POST", "/api/v1/agents/enroll", gatePublic},
	{"GET", "/api/v1/invite/{token}", gatePublic},
	{"POST", "/api/v1/invite/{token}/accept", gatePublic},
	{"POST", "/api/v1/stacks/{name}/git/webhook", gatePublic},
	{"GET", "/api/v1/me", gateSignedIn},
	{"PUT", "/api/v1/users/{id}/password", gateSignedIn},
	{"GET", "/api/v1/users/{id}/avatar", gateSignedIn},
	{"POST", "/api/v1/users/{id}/avatar", gateSignedIn},
	{"DELETE", "/api/v1/users/{id}/avatar", gateSignedIn},
	{"POST", "/api/v1/ws/ticket", gateSignedIn},
	{"GET", "/api/v1/sessions", gateSignedIn},
	{"DELETE", "/api/v1/sessions/{family_id}", gateSignedIn},
	{"POST", "/api/v1/sessions/revoke-all", gateSignedIn},
	{"POST", "/api/v1/mfa/enroll/start", gateSignedIn},
	{"POST", "/api/v1/mfa/enroll/verify", gateSignedIn},
	{"DELETE", "/api/v1/mfa", gateSignedIn},
	{"GET", "/api/v1/notifications", gateSignedIn},
	{"GET", "/api/v1/notifications/unread-count", gateSignedIn},
	{"POST", "/api/v1/notifications/{id}/read", gateSignedIn},
	{"POST", "/api/v1/notifications/read-all", gateSignedIn},
	{"DELETE", "/api/v1/notifications/{id}", gateSignedIn},
	{"GET", "/api/v1/notices", gateSignedIn},
	{"POST", "/api/v1/notices/{id}/dismiss", gateSignedIn},
	{"GET", "/api/v1/system/whats-new", gateSignedIn},
	{"POST", "/api/v1/system/whats-new/seen", gateSignedIn},
	{"GET", "/api/v1/attention", gateSignedIn},
	{"GET", "/api/v1/operations", gateSignedIn},
	{"GET", "/api/v1/operations/{id}", gateSignedIn},
	{"POST", "/api/v1/operations/{id}/cancel", gateSignedIn},
	{"GET", "/api/v1/hosts", rbac.PermHostsView},
	{"GET", "/api/v1/stacks", rbac.PermStacksView},
	{"GET", "/api/v1/stacks/discovered", rbac.PermStacksView},
	{"GET", "/api/v1/stacks/{name}", rbac.PermStacksView},
	{"GET", "/api/v1/stacks/{name}/status", rbac.PermStacksView},
	{"GET", "/api/v1/stacks/{name}/deploy/progress", rbac.PermStacksView},
	{"GET", "/api/v1/containers", rbac.PermContainersView},
	{"GET", "/api/v1/containers/summary", rbac.PermContainersView},
	{"GET", "/api/v1/containers/{id}", rbac.PermContainersView},
	{"GET", "/api/v1/containers/{id}/metrics", rbac.PermContainersView},
	{"GET", "/api/v1/hosts/{id}/stats/containers", rbac.PermContainersView},
	{"GET", "/api/v1/containers/{id}/files", rbac.PermContainersFiles},
	{"GET", "/api/v1/containers/{id}/files/content", rbac.PermContainersFiles},
	{"GET", "/api/v1/containers/{id}/files/download", rbac.PermContainersFiles},
	{"POST", "/api/v1/containers/{id}/files", rbac.PermContainersExec},
	{"GET", "/api/v1/images", rbac.PermImagesView},
	{"GET", "/api/v1/images/updates", rbac.PermImagesView},
	{"GET", "/api/v1/images/{id}", rbac.PermImagesView},
	{"GET", "/api/v1/networks", rbac.PermNetworksView},
	{"GET", "/api/v1/networks/topology", rbac.PermNetworksView},
	{"GET", "/api/v1/networks/{id}", rbac.PermNetworksView},
	{"GET", "/api/v1/volumes", rbac.PermVolumesView},
	{"GET", "/api/v1/volumes/{name}", rbac.PermVolumesView},
	{"GET", "/api/v1/system/metrics", rbac.PermHostsView},
	{"GET", "/api/v1/system/backup-status", rbac.PermSystemView},
	{"GET", "/api/v1/system/health", rbac.PermSystemView},
	{"GET", "/api/v1/system/info", rbac.PermSystemView},
	{"GET", "/api/v1/system/update-status", rbac.PermSystemView},
	{"POST", "/api/v1/stacks", rbac.PermStacksCreate},
	{"POST", "/api/v1/stacks/from-git", rbac.PermStacksCreate},
	{"POST", "/api/v1/convert/run-to-compose", rbac.PermStacksCreate},
	{"PUT", "/api/v1/stacks/{name}", rbac.PermStacksUpdate},
	{"POST", "/api/v1/stacks/{name}/recover", rbac.PermStacksUpdate},
	{"DELETE", "/api/v1/stacks/{name}", rbac.PermStacksDelete},
	{"GET", "/api/v1/stacks/{name}/cleanup-preview", rbac.PermStacksDelete},
	{"POST", "/api/v1/stacks/{name}/discard", rbac.PermStacksDelete},
	{"POST", "/api/v1/stacks/adopt", rbac.PermStacksAdopt},
	{"POST", "/api/v1/stacks/{name}/git", rbac.PermStacksGit},
	{"DELETE", "/api/v1/stacks/{name}/git", rbac.PermStacksGit},
	{"GET", "/api/v1/stacks/{name}/git", rbac.PermStacksView},
	{"POST", "/api/v1/stacks/{name}/git/sync", rbac.PermStacksDeploy},
	{"POST", "/api/v1/templates", rbac.PermTemplatesCreate},
	{"PUT", "/api/v1/templates/{id}", rbac.PermTemplatesUpdate},
	{"DELETE", "/api/v1/templates/{id}", rbac.PermTemplatesDelete},
	{"POST", "/api/v1/templates/{id}/deploy", rbac.PermStacksDeploy},
	{"GET", "/api/v1/templates", rbac.PermTemplatesView},
	{"GET", "/api/v1/templates/{id}", rbac.PermTemplatesView},
	{"GET", "/api/v1/templates/{id}/export", rbac.PermTemplatesView},
	{"POST", "/api/v1/stacks/{name}/deploy", rbac.PermStacksDeploy},
	{"GET", "/api/v1/stacks/{name}/scale", rbac.PermStacksDeploy},
	{"GET", "/api/v1/stacks/{name}/services/{service}/scale", rbac.PermStacksDeploy},
	{"POST", "/api/v1/stacks/{name}/services/{service}/scale", rbac.PermStacksDeploy},
	{"POST", "/api/v1/stacks/{name}/services/{service}/rolling-update", rbac.PermStacksDeploy},
	{"GET", "/api/v1/stacks/{name}/deployments", rbac.PermStacksDeploy},
	{"GET", "/api/v1/stacks/{name}/deployments/{id}", rbac.PermStacksDeploy},
	{"POST", "/api/v1/stacks/{name}/deployments/{id}/rollback", rbac.PermStacksDeploy},
	{"GET", "/api/v1/stacks/{name}/dependencies", rbac.PermStacksDeploy},
	{"PUT", "/api/v1/stacks/{name}/dependencies", rbac.PermStacksDeploy},
	{"GET", "/api/v1/stacks/{name}/environments", rbac.PermStacksDeploy},
	{"PUT", "/api/v1/stacks/{name}/environments/active", rbac.PermStacksDeploy},
	{"GET", "/api/v1/stacks/{name}/scaling-rules", rbac.PermStacksDeploy},
	{"PUT", "/api/v1/stacks/{name}/scaling-rules", rbac.PermStacksDeploy},
	{"DELETE", "/api/v1/stacks/{name}/scaling-rules", rbac.PermStacksDeploy},
	{"POST", "/api/v1/stacks/{name}/stop", rbac.PermStacksOperate},
	{"POST", "/api/v1/stacks/{name}/migrate", rbac.PermStacksMigrate},
	{"POST", "/api/v1/stacks/{name}/migrate/preflight", rbac.PermStacksMigrate},
	{"GET", "/api/v1/stacks/{name}/migrate/{id}", rbac.PermStacksMigrate},
	{"POST", "/api/v1/stacks/{name}/migrate/{id}/rollback", rbac.PermStacksMigrate},
	{"DELETE", "/api/v1/stacks/{name}/migrate/{id}/source", rbac.PermStacksMigrate},
	{"GET", "/api/v1/migrations", gateSignedIn},
	{"GET", "/api/v1/migrations/active", gateSignedIn},
	{"GET", "/api/v1/settings", rbac.PermSystemUpdate},
	{"PUT", "/api/v1/settings", rbac.PermSystemUpdate},
	{"GET", "/api/v1/global-env", rbac.PermSystemUpdate},
	{"POST", "/api/v1/global-env", rbac.PermSystemUpdate},
	{"PUT", "/api/v1/global-env/{id}", rbac.PermSystemUpdate},
	{"DELETE", "/api/v1/global-env/{id}", rbac.PermSystemUpdate},
	{"GET", "/api/v1/global-env/groups", rbac.PermSystemUpdate},
	{"GET", "/api/v1/global-env/{id}/refs", rbac.PermSystemUpdate},
	{"GET", "/api/v1/roles", rbac.PermRolesView},
	{"GET", "/api/v1/roles/catalog", rbac.PermRolesView},
	{"GET", "/api/v1/roles/{name}", rbac.PermRolesView},
	{"POST", "/api/v1/roles", rbac.PermRolesCreate},
	{"PUT", "/api/v1/roles/{name}", rbac.PermRolesUpdate},
	{"DELETE", "/api/v1/roles/{name}", rbac.PermRolesDelete},
	{"GET", "/api/v1/settings/registries", rbac.PermRegistriesView},
	{"POST", "/api/v1/settings/registries", rbac.PermRegistriesCreate},
	{"PUT", "/api/v1/settings/registries/{id}", rbac.PermRegistriesUpdate},
	{"POST", "/api/v1/settings/registries/{id}/test", rbac.PermRegistriesUpdate},
	{"DELETE", "/api/v1/settings/registries/{id}", rbac.PermRegistriesDelete},
	{"GET", "/api/v1/hosts/tags/all", gateSignedIn},
	{"GET", "/api/v1/hosts/{id}/tags", gateSignedIn},
	{"PUT", "/api/v1/hosts/{id}/tags", rbac.PermHostsTag},
	{"POST", "/api/v1/hosts/{id}/tags", rbac.PermHostsTag},
	{"DELETE", "/api/v1/hosts/{id}/tags/{tag}", rbac.PermHostsTag},
	{"POST", "/api/v1/hosts/{id}/drain/plan", rbac.PermHostsUpdate},
	{"POST", "/api/v1/hosts/{id}/drain/execute", rbac.PermHostsUpdate},
	{"GET", "/api/v1/hosts/{id}/drain/{drain_id}", rbac.PermHostsUpdate},
	{"POST", "/api/v1/hosts/{id}/drain/{drain_id}/pause", rbac.PermHostsUpdate},
	{"POST", "/api/v1/hosts/{id}/drain/{drain_id}/resume", rbac.PermHostsUpdate},
	{"POST", "/api/v1/hosts/{id}/drain/{drain_id}/abort", rbac.PermHostsUpdate},
	{"POST", "/api/v1/containers/{id}/start", rbac.PermContainersUpdate},
	{"POST", "/api/v1/containers/{id}/stop", rbac.PermContainersUpdate},
	{"POST", "/api/v1/containers/{id}/restart", rbac.PermContainersUpdate},
	{"POST", "/api/v1/containers/{id}/pause", rbac.PermContainersUpdate},
	{"POST", "/api/v1/containers/{id}/unpause", rbac.PermContainersUpdate},
	{"POST", "/api/v1/containers/{id}/kill", rbac.PermContainersUpdate},
	{"DELETE", "/api/v1/containers/{id}", rbac.PermContainersDelete},
	{"GET", "/api/v1/containers/{id}/update-info", rbac.PermContainersUpgrade},
	{"POST", "/api/v1/containers/{id}/update", rbac.PermContainersUpgrade},
	{"POST", "/api/v1/containers/updates", rbac.PermContainersUpgrade},
	{"POST", "/api/v1/containers/{id}/rollback", rbac.PermContainersUpgrade},
	{"GET", "/api/v1/containers/{id}/update-history", rbac.PermContainersUpgrade},
	{"POST", "/api/v1/images/pull", rbac.PermImagesCreate},
	{"DELETE", "/api/v1/images/{id}", rbac.PermImagesDelete},
	{"POST", "/api/v1/images/prune", rbac.PermImagesDelete},
	{"POST", "/api/v1/images/{id}/scan", rbac.PermImagesScan},
	{"GET", "/api/v1/images/{id}/scan", rbac.PermImagesScan},
	{"POST", "/api/v1/networks", rbac.PermNetworksCreate},
	{"DELETE", "/api/v1/networks/{id}", rbac.PermNetworksDelete},
	{"POST", "/api/v1/networks/prune", rbac.PermNetworksDelete},
	{"POST", "/api/v1/volumes", rbac.PermVolumesCreate},
	{"DELETE", "/api/v1/volumes/{name}", rbac.PermVolumesDelete},
	{"POST", "/api/v1/volumes/prune", rbac.PermVolumesDelete},
	{"GET", "/api/v1/volumes/{name}/browse", rbac.PermVolumesBrowse},
	{"GET", "/api/v1/volumes/{name}/browse/file", rbac.PermVolumesBrowse},
	{"POST", "/api/v1/restore/verify", rbac.PermBackupsRestore},
	{"POST", "/api/v1/backups/runs/{id}/verify", rbac.PermBackupsRestore},
	{"GET", "/api/v1/users", rbac.PermUsersView},
	{"GET", "/api/v1/users/{id}", rbac.PermUsersView},
	{"GET", "/api/v1/assignments", rbac.PermUsersView},
	{"POST", "/api/v1/access/check", rbac.PermUsersView},
	{"POST", "/api/v1/users", rbac.PermUsersCreate},
	{"GET", "/api/v1/invites", rbac.PermUsersCreate},
	{"POST", "/api/v1/invites", rbac.PermUsersCreate},
	{"DELETE", "/api/v1/invites/{id}", rbac.PermUsersCreate},
	{"PUT", "/api/v1/users/{id}", rbac.PermUsersUpdate},
	{"POST", "/api/v1/users/{id}/unlock", rbac.PermUsersUpdate},
	{"POST", "/api/v1/assignments", rbac.PermUsersUpdate},
	{"DELETE", "/api/v1/assignments/{id}", rbac.PermUsersUpdate},
	{"DELETE", "/api/v1/users/{id}", rbac.PermUsersDelete},
	{"DELETE", "/api/v1/users/{id}/mfa", rbac.PermUsersDelete},
	{"GET", "/api/v1/auth/policy", rbac.PermSystemUpdate},
	{"PUT", "/api/v1/auth/policy", rbac.PermSystemUpdate},
	{"GET", "/api/v1/auth/signin", rbac.PermSystemUpdate},
	{"PUT", "/api/v1/auth/signin", rbac.PermSystemUpdate},
	{"GET", "/api/v1/audit", rbac.PermAuditView},
	{"GET", "/api/v1/audit/verify", rbac.PermAuditView},
	{"GET", "/api/v1/audit/retention", rbac.PermAuditView},
	{"PUT", "/api/v1/audit/retention", rbac.PermAuditWrite},
	{"POST", "/api/v1/audit/retention/run", rbac.PermAuditWrite},
	{"GET", "/api/v1/audit/webhook", rbac.PermAuditWrite},
	{"PUT", "/api/v1/audit/webhook", rbac.PermAuditWrite},
	{"POST", "/api/v1/audit/webhook/test", rbac.PermAuditWrite},
	{"GET", "/api/v1/auth/providers", rbac.PermSystemUpdate},
	{"GET", "/api/v1/oidc/providers", rbac.PermSystemUpdate},
	{"POST", "/api/v1/oidc/providers", rbac.PermSystemUpdate},
	{"PUT", "/api/v1/oidc/providers/{id}", rbac.PermSystemUpdate},
	{"DELETE", "/api/v1/oidc/providers/{id}", rbac.PermSystemUpdate},
	{"POST", "/api/v1/oidc/providers/reload", rbac.PermSystemUpdate},
	{"POST", "/api/v1/oidc/providers/test-discovery", rbac.PermSystemUpdate},
	{"POST", "/api/v1/oidc/providers/{id}/test", rbac.PermSystemUpdate},
	{"GET", "/api/v1/saml/providers", rbac.PermSystemUpdate},
	{"GET", "/api/v1/saml/providers/{id}", rbac.PermSystemUpdate},
	{"POST", "/api/v1/saml/providers", rbac.PermSystemUpdate},
	{"PUT", "/api/v1/saml/providers/{id}", rbac.PermSystemUpdate},
	{"DELETE", "/api/v1/saml/providers/{id}", rbac.PermSystemUpdate},
	{"POST", "/api/v1/saml/providers/{id}/test", rbac.PermSystemUpdate},
	{"GET", "/api/v1/saml/rekey", rbac.PermSystemUpdate},
	{"POST", "/api/v1/saml/rekey/end", rbac.PermSystemUpdate},
	{"GET", "/api/v1/ldap/providers", rbac.PermSystemUpdate},
	{"GET", "/api/v1/ldap/providers/{id}", rbac.PermSystemUpdate},
	{"POST", "/api/v1/ldap/providers", rbac.PermSystemUpdate},
	{"PUT", "/api/v1/ldap/providers/{id}", rbac.PermSystemUpdate},
	{"DELETE", "/api/v1/ldap/providers/{id}", rbac.PermSystemUpdate},
	{"POST", "/api/v1/ldap/providers/{id}/test", rbac.PermSystemUpdate},
	{"GET", "/api/v1/oauth2/providers", rbac.PermSystemUpdate},
	{"GET", "/api/v1/oauth2/providers/{id}", rbac.PermSystemUpdate},
	{"POST", "/api/v1/oauth2/providers", rbac.PermSystemUpdate},
	{"PUT", "/api/v1/oauth2/providers/{id}", rbac.PermSystemUpdate},
	{"DELETE", "/api/v1/oauth2/providers/{id}", rbac.PermSystemUpdate},
	{"POST", "/api/v1/oauth2/providers/{id}/test", rbac.PermSystemUpdate},
	{"GET", "/api/v1/notifications/channels", rbac.PermAlertsView},
	{"GET", "/api/v1/alerts/rules", rbac.PermAlertsView},
	{"GET", "/api/v1/alerts/rules/{id}/stats", rbac.PermAlertsView},
	{"GET", "/api/v1/alerts/history", rbac.PermAlertsHistory},
	{"POST", "/api/v1/notifications/channels", rbac.PermAlertsCreate},
	{"POST", "/api/v1/alerts/rules", rbac.PermAlertsCreate},
	{"PUT", "/api/v1/notifications/channels/{id}", rbac.PermAlertsUpdate},
	{"POST", "/api/v1/notifications/channels/{id}/test", rbac.PermAlertsUpdate},
	{"PUT", "/api/v1/alerts/rules/{id}", rbac.PermAlertsUpdate},
	{"DELETE", "/api/v1/notifications/channels/{id}", rbac.PermAlertsDelete},
	{"DELETE", "/api/v1/alerts/rules/{id}", rbac.PermAlertsDelete},
	{"GET", "/api/v1/agents", rbac.PermHostsView},
	{"GET", "/api/v1/agents/{id}", rbac.PermHostsView},
	{"GET", "/api/v1/agents/upgrade-policy", rbac.PermHostsView},
	{"POST", "/api/v1/agents", rbac.PermHostsCreate},
	{"POST", "/api/v1/agents/{id}/rotate-token", rbac.PermHostsCreate},
	{"POST", "/api/v1/agents/{id}/upgrade", rbac.PermHostsUpdate},
	{"PUT", "/api/v1/agents/upgrade-policy", rbac.PermHostsUpdate},
	{"POST", "/api/v1/agents/upgrade-policy/run", rbac.PermHostsUpdate},
	{"DELETE", "/api/v1/agents/{id}", rbac.PermHostsDelete},
	{"GET", "/api/v1/backups/jobs", rbac.PermBackupsView},
	{"GET", "/api/v1/backups/jobs/{id}", rbac.PermBackupsView},
	{"GET", "/api/v1/backups/targets", rbac.PermBackupsView},
	{"GET", "/api/v1/backups/local-path", rbac.PermBackupsView},
	{"GET", "/api/v1/backups/runs", rbac.PermBackupsView},
	{"GET", "/api/v1/backups/schedule", rbac.PermBackupsView},
	{"GET", "/api/v1/backups/runs/{id}", rbac.PermBackupsView},
	{"GET", "/api/v1/backups/runs/{id}/log", rbac.PermBackupsView},
	{"GET", "/api/v1/backups/runs/{id}/archive", rbac.PermBackupsDownload},
	{"POST", "/api/v1/backups/jobs", rbac.PermBackupsCreate},
	{"POST", "/api/v1/backups/targets", rbac.PermBackupsCreate},
	{"POST", "/api/v1/backups/targets/test-config", rbac.PermBackupsCreate},
	{"POST", "/api/v1/backups/targets/discover-shares", rbac.PermBackupsCreate},
	{"PUT", "/api/v1/backups/jobs/{id}", rbac.PermBackupsUpdate},
	{"POST", "/api/v1/backups/jobs/{id}/accept-host-key", rbac.PermBackupsUpdate},
	{"POST", "/api/v1/backups/jobs/{id}/review/{mode}", rbac.PermBackupsUpdate},
	{"PUT", "/api/v1/backups/targets/{id}", rbac.PermBackupsUpdate},
	{"POST", "/api/v1/backups/targets/{id}/test", rbac.PermBackupsUpdate},
	{"POST", "/api/v1/backups/targets/{id}/accept-host-key", rbac.PermBackupsUpdate},
	{"PUT", "/api/v1/backups/system/enabled", rbac.PermBackupsUpdate},
	{"DELETE", "/api/v1/backups/jobs/{id}", rbac.PermBackupsDelete},
	{"DELETE", "/api/v1/backups/targets/{id}", rbac.PermBackupsDelete},
	{"POST", "/api/v1/backups/jobs/{id}/run", rbac.PermBackupsRun},
	{"POST", "/api/v1/backups/runs/{id}/restore", rbac.PermBackupsRestore},
	{"POST", "/api/v1/backups/runs/{id}/restore-stack", rbac.PermBackupsRestore},
	{"GET", "/api/v1/system/backup-key/export", rbac.PermBackupsKeyExport},
	{"POST", "/api/v1/system/secrets/rotate", rbac.PermSystemUpgrade},
	{"POST", "/api/v1/system/update-check", rbac.PermSystemUpgrade},
	{"GET", "/api/v1/restore/system", rbac.PermSystemUpgrade},
	{"POST", "/api/v1/restore/system", rbac.PermSystemUpgrade},
	{"DELETE", "/api/v1/restore/system", rbac.PermSystemUpgrade},
	{"POST", "/api/v1/restore/system/apply", rbac.PermSystemUpgrade},
	{"GET", "/api/v1/proxy/status", rbac.PermProxyView},
	{"GET", "/api/v1/proxy/routes", rbac.PermProxyView},
	{"GET", "/api/v1/proxy/routes/{id}/metrics", rbac.PermProxyView},
	{"GET", "/api/v1/proxy/routes/{id}/config", rbac.PermProxyView},
	{"GET", "/api/v1/proxy/metrics", rbac.PermProxyView},
	{"GET", "/api/v1/proxy/acme-events", rbac.PermProxyView},
	{"GET", "/api/v1/proxy/version", rbac.PermProxyView},
	{"GET", "/api/v1/proxy/settings", rbac.PermProxyView},
	{"POST", "/api/v1/proxy/routes", rbac.PermProxyCreate},
	{"POST", "/api/v1/proxy/import", rbac.PermProxyCreate},
	{"POST", "/api/v1/proxy/enable", rbac.PermProxyCreate},
	{"PUT", "/api/v1/proxy/routes/{id}", rbac.PermProxyUpdate},
	{"PATCH", "/api/v1/proxy/routes/{id}/enabled", rbac.PermProxyUpdate},
	{"PUT", "/api/v1/proxy/version", rbac.PermProxyUpdate},
	{"POST", "/api/v1/proxy/version/rollback", rbac.PermProxyUpdate},
	{"PUT", "/api/v1/proxy/settings", rbac.PermProxyUpdate},
	{"DELETE", "/api/v1/proxy/routes/{id}", rbac.PermProxyDelete},
	{"POST", "/api/v1/proxy/disable", rbac.PermProxyDelete},
	{"GET", "/api/v1/ws/logs/{id}", gateTicket},
	{"GET", "/api/v1/ws/events", gateTicket},
	{"GET", "/api/v1/ws/exec/{id}", gateTicket},
	{"GET", "/api/v1/ws/stats/{id}", gateTicket},
	{"GET", "/install/agent.sh", gatePublic},
	{"GET", "/install/{name}", gatePublic},
	{"POST", "/api/v1/setup/validate-data-dir", gatePublic},
	{"POST", "/api/v1/setup/validate-user", gatePublic},
	{"POST", "/api/v1/setup/test-url", gatePublic},
	{"POST", "/api/v1/setup/commit", gatePublic},
	{"POST", "/api/v1/setup/restore", gatePublic},
	{"GET", "/api/v1/settings/api-tokens", rbac.PermTokensView},
	{"POST", "/api/v1/settings/api-tokens", rbac.PermTokensCreate},
	{"DELETE", "/api/v1/settings/api-tokens/{id}", rbac.PermTokensDelete},
}

func newMatrixEnv(t *testing.T) (*auth.Service, *rbac.Store, *handlers.Handlers) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	authSvc := auth.NewService(database, []byte("0123456789abcdef0123456789abcdef"))
	roles := rbac.NewStore(database)
	if err := roles.Load(ctx); err != nil {
		t.Fatal(err)
	}
	authSvc.SetAccessHooks(func(ctx context.Context) {
		if err := roles.Load(ctx); err != nil {
			t.Error(err)
		}
	}, roles.ReplaceIdPAssignments)
	prev := middleware.RBACStore
	middleware.RBACStore = roles
	t.Cleanup(func() { middleware.RBACStore = prev })
	mgr, err := stacks.NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := handlers.New(handlers.Deps{Auth: authSvc, Roles: roles, Stacks: mgr, Deployments: stacks.NewDeploymentStore(database)})
	return authSvc, roles, h
}

func TestRouteGatesCoverEveryRoute(t *testing.T) {
	authSvc, _, h := newMatrixEnv(t)
	router := NewRouter(h, authSvc, nil, true, nil)
	want := map[string]bool{}
	for _, g := range routeGates {
		want[g.method+" "+g.path] = true
	}
	got := map[string]bool{}
	if err := chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var missing, stale []string
	for k := range got {
		if !want[k] {
			missing = append(missing, k)
		}
	}
	for k := range want {
		if !got[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	for _, k := range missing {
		t.Errorf("route %s has no entry in routeGates", k)
	}
	for _, k := range stale {
		t.Errorf("routeGates entry %s matches no route", k)
	}
}

func fillPath(p string) string {
	var b strings.Builder
	for {
		i := strings.Index(p, "{")
		if i < 0 {
			b.WriteString(p)
			return b.String()
		}
		j := strings.Index(p[i:], "}")
		b.WriteString(p[:i])
		switch p[i+1 : i+j] {
		case "name":
			b.WriteString("web")
		case "id", "drain_id", "run_id":
			b.WriteString("1")
		default:
			b.WriteString("x")
		}
		p = p[i+j+1:]
	}
}

type matrixCaller struct {
	name  string
	perms map[rbac.Perm]bool
	scope rbac.Scope
	token string
}

func signIn(t *testing.T, authSvc *auth.Service, roles *rbac.Store, name, role string, scope rbac.Scope) string {
	t.Helper()
	ctx := context.Background()
	u, err := authSvc.CreateUser(ctx, name, "", "correct horse battery", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roles.AddAssignment(ctx, rbac.Assignment{UserID: u.ID, Role: role, Scope: scope}); err != nil {
		t.Fatal(err)
	}
	res, err := authSvc.Login(ctx, name, "correct horse battery", "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return res.AccessToken
}

// Every built-in role, assigned on everything and on the local host, runs
// against every permission-gated route: the route gate lets it through
// exactly when the role holds the permission and, for instance-wide
// permissions, the assignment is on everything. The stack routes act on
// a stack deployed on the local host. Unauthenticated calls get 401.
func TestRouteGateMatrix(t *testing.T) {
	authSvc, roles, h := newMatrixEnv(t)
	router := NewRouter(h, authSvc, nil, true, nil)
	var callers []matrixCaller
	for _, b := range rbac.BuiltinRoles {
		for _, scope := range []rbac.Scope{rbac.Global, {Type: rbac.ScopeHost, Host: "local"}} {
			name := b.Name + "-" + scope.Type
			callers = append(callers, matrixCaller{name: name, perms: rbac.PermsFor(b.Levels, b.Extras), scope: scope,
				token: signIn(t, authSvc, roles, name, b.Name, scope)})
		}
	}

	call := func(method, path, token string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rr := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = recover() }()
			router.ServeHTTP(rr, req)
		}()
		select {
		case <-done:
			return rr.Code, strings.TrimSpace(rr.Body.String())
		case <-time.After(5 * time.Second):
			return 0, "timeout"
		}
	}

	for _, g := range routeGates {
		if g.gate == gatePublic || g.gate == gateTicket {
			continue
		}
		path := fillPath(g.path)
		if code, _ := call(g.method, path, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", g.method, g.path, code)
		}
		if g.gate == gateSignedIn {
			continue
		}
		for _, c := range callers {
			// Earlier routes delete or stop the stack.
			if err := h.Deployments.Set(context.Background(), "web", "local", "running"); err != nil {
				t.Fatal(err)
			}
			want := rbac.IsBase(g.gate) || (c.perms[g.gate] && (c.scope.IsGlobal() || !rbac.IsGlobalOnly(g.gate)))
			code, body := call(g.method, path, c.token)
			denied := code == http.StatusForbidden && (body == "forbidden" || strings.Contains(body, "out of scope"))
			if denied == want {
				t.Errorf("%s %s as %s: %d %s, want allowed=%v", g.method, g.path, c.name, code, body, want)
			}
		}
	}
}

// List endpoints return only what an assignment reaches; a stack that
// exists only as files reaches nobody but assignments on everything or
// on that stack.
func TestStackListFollowsScope(t *testing.T) {
	authSvc, roles, h := newMatrixEnv(t)
	ctx := context.Background()
	for _, name := range []string{"onlocal", "onagent", "filesonly"} {
		if _, err := h.Stacks.Create(name, "services:\n  app:\n    image: nginx\n", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Deployments.Set(ctx, "onlocal", "local", "running"); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployments.Set(ctx, "onagent", "agent-1", "running"); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(h, authSvc, nil, true, nil)
	tests := []struct {
		user  string
		scope rbac.Scope
		want  string
	}{
		{"everything", rbac.Global, "filesonly,onagent,onlocal"},
		{"agent", rbac.Scope{Type: rbac.ScopeHost, Host: "agent-1"}, "onagent"},
		{"stack", rbac.Scope{Type: rbac.ScopeStack, Host: "local", Value: "onlocal"}, "onlocal"},
		{"draft", rbac.Scope{Type: rbac.ScopeStack, Value: "filesonly"}, "filesonly"},
		{"draft-on-host", rbac.Scope{Type: rbac.ScopeStack, Host: "local", Value: "filesonly"}, ""},
		{"tag", rbac.Scope{Type: rbac.ScopeHostTag, Value: "prod"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			token := signIn(t, authSvc, roles, tt.user, rbac.RoleViewer, tt.scope)
			get := func(path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer "+token)
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				return rr
			}
			rr := get("/api/v1/stacks")
			if rr.Code != http.StatusOK {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
			var list []struct{ Name string }
			if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, s := range list {
				names = append(names, s.Name)
			}
			sort.Strings(names)
			if got := strings.Join(names, ","); got != tt.want {
				t.Errorf("stacks %q, want %q", got, tt.want)
			}
			for _, name := range []string{"filesonly", "onagent", "onlocal"} {
				listed := strings.Contains(","+tt.want+",", ","+name+",")
				if code := get("/api/v1/stacks/" + name).Code; (code == http.StatusOK) != listed {
					t.Errorf("GET /stacks/%s: %d, listed=%v", name, code, listed)
				}
			}
		})
	}
}
