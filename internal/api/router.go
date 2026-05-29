package api

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/setup"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func NewRouter(h *handlers.Handlers, authSvc *auth.Service, webFS fs.FS, metricsAuth bool, setupState *setup.State) http.Handler {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(middleware.Logging)
	r.Use(chimw.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: true,
	}))
	// P.14.1: gate non-setup paths while setup mode is active. Returns
	// 503 with a body pointing UI clients at /setup, lets the wizard
	// itself + its API + static assets through.
	r.Use(middleware.SetupGate(setupState))

	// Kubernetes-style health probes (P.12.2). Mounted at the router
	// root, unauthenticated, so k8s / docker-compose healthchecks /
	// any load balancer reach them without an API version or token.
	// /healthz/live = process alive. /healthz/ready = DB pingable AND
	// not draining. Load balancers flip /ready to 503 during SIGTERM
	// drain so new requests route elsewhere while in-flight ones finish.
	r.Get("/healthz/live", h.Live)
	r.Get("/healthz/ready", h.Ready)

	// P.11.9 — Prometheus scrape endpoint mounted at router root so
	// operators configure prometheus.yml with just the host (no API
	// version in the path). When metricsAuth is true (default), the
	// scraper must present a Bearer token with metrics.read perm.
	if metricsAuth {
		r.Group(func(r chi.Router) {
			r.Use(middleware.NewAuth(authSvc))
			r.Use(middleware.RequirePerm(rbac.PermMetricsView))
			r.Get("/metrics", h.PromMetrics)
		})
	} else {
		r.Get("/metrics", h.PromMetrics)
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.Health)

		// P.14.1+P.14.2: install-wizard endpoints. All public — the
		// wizard runs before any auth exists. The gate middleware lets
		// /setup/* through automatically.
		r.Get("/setup/status", h.SetupStatus)
		r.Get("/setup/preflight", h.SetupPreflight)
		r.Get("/setup/server-info", h.SetupServerInfo)
		r.Post("/setup/validate-data-dir", h.SetupValidateDataDir)
		r.Post("/setup/validate-user", h.SetupValidateUser)
		r.Post("/setup/test-url", h.SetupTestURL)
		r.Post("/setup/commit", h.SetupCommit)
		r.Get("/setup/stream/{run_id}", h.SetupStream)

		// OpenAPI 3.1 spec + Swagger UI (P.11.10). Public — the spec
		// lists only endpoint shapes, not secrets; clients need it to
		// integrate against the API.
		r.Get("/openapi.json", h.ServeOpenAPIJSON)
		r.Get("/openapi.yaml", h.ServeOpenAPIYAML)
		r.Get("/docs", h.ServeSwaggerUI)

		r.Post("/auth/login", h.Login)
		r.Post("/auth/mfa", h.LoginMFA)
		r.Post("/auth/logout", h.Logout)
		r.Post("/auth/refresh", h.Refresh)

		// OIDC flow is public — state is carried in a signed cookie.
		r.Get("/auth/oidc/providers", h.ListOIDCProvidersPublic)
		r.Get("/auth/oidc/{slug}/login", h.OIDCLogin)
		r.Get("/auth/oidc/{slug}/callback", h.OIDCCallback)

		// SAML flow is also public. ACS endpoint receives a signed
		// POST from the IdP; we validate against the cert we extracted
		// from idp metadata. Metadata endpoint is public so IdP
		// admins can fetch the SP descriptor without auth.
		r.Get("/auth/saml/providers", h.ListSAMLProvidersPublic)
		r.Get("/auth/saml/{slug}/login", h.SAMLLogin)
		r.Post("/auth/saml/{slug}/acs", h.SAMLACS)
		r.Get("/auth/saml/{slug}/metadata", h.SAMLMetadata)

		// LDAP login takes the user's password directly — no
		// redirect round-trip. The endpoint mints a session pair
		// just like /auth/login does for local accounts.
		r.Get("/auth/ldap/providers", h.ListLDAPProvidersPublic)
		r.Post("/auth/ldap/{slug}/login", h.LDAPLogin)

		// Generic OAuth2 (non-OIDC) — GitHub / Bitbucket / etc.
		r.Get("/auth/oauth2/providers", h.ListOAuth2ProvidersPublic)
		r.Get("/auth/oauth2/{slug}/login", h.OAuth2Login)
		r.Get("/auth/oauth2/{slug}/callback", h.OAuth2Callback)

		// Agent enrollment — token is the auth, no JWT required.
		r.Post("/agents/enroll", h.EnrollAgent)

		// Invite-link accept flow — token is the auth. Preview shows
		// "Invitation as <role> for <email_hint>" to the recipient;
		// accept consumes the token + provisions the user account.
		r.Get("/invite/{token}", h.PreviewInvite)
		r.Post("/invite/{token}/accept", h.AcceptInvite)

		// Git webhook endpoint (P.11.11). Public — GitHub / GitLab /
		// Gitea cannot send Bearer tokens. Signature verification is
		// done inside the handler using the stack's stored webhook
		// secret when one is configured.
		r.Post("/stacks/{name}/git/webhook", h.GitWebhook)

		r.Group(func(r chi.Router) {
			r.Use(middleware.NewAuth(authSvc))

			// Self-service routes (any authenticated user)
			r.Get("/me", h.Me)
			r.Put("/users/{id}/password", h.ChangeUserPassword) // self or admin (enforced inside)
			r.Get("/users/{id}/avatar", h.GetAvatar)              // any authed user
			r.Post("/users/{id}/avatar", h.UploadAvatar)          // self or admin (enforced inside)
			r.Delete("/users/{id}/avatar", h.DeleteAvatar)        // self or admin (enforced inside)
			r.Post("/ws/ticket", h.WSTicket)

			// P.12.1 — session management (self). Any authenticated
			// user can see + revoke their own sessions.
			r.Get("/sessions", h.ListMySessions)
			r.Delete("/sessions/{family_id}", h.RevokeMySession)
			r.Post("/sessions/revoke-all", h.RevokeAllMySessions)

			// Self MFA enrollment / disable
			r.Post("/mfa/enroll/start", h.MFAEnrollStart)
			r.Post("/mfa/enroll/verify", h.MFAEnrollVerify)
			r.Delete("/mfa", h.MFADisable)

			// Notification center (bell icon). Per-user feed —
			// the service filters own + broadcast rows internally.
			r.Get("/notifications", h.ListNotifications)
			r.Get("/notifications/unread-count", h.NotificationsUnreadCount)
			r.Post("/notifications/{id}/read", h.MarkNotificationRead)
			r.Post("/notifications/read-all", h.MarkAllNotificationsRead)
			r.Delete("/notifications/{id}", h.DeleteNotification)

			// -------------------------- READ ROUTES --------------------------
			// Per-resource *.view permissions (RBAC v2). Each group gates
			// its own listing + inspect endpoints. Built-in viewer role
			// has every *.view granted; other roles inherit them.

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermHostsView))
				// Host registry — local + every connected agent.
				r.Get("/hosts", h.ListHosts)
			})

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStacksView))
				r.Get("/stacks", h.ListStacks)
				// /stacks/discovered must be registered BEFORE /stacks/{name}
				// so chi routes the literal path to the discovery handler
				// instead of treating "discovered" as a stack name.
				r.Get("/stacks/discovered", h.DiscoverStacks)
				r.Get("/stacks/{name}", h.GetStack)
				r.Get("/stacks/{name}/status", h.StackStatus)
				r.Get("/stacks/{name}/deploy/progress", h.GetDeployProgress)
			})

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermContainersView))
				r.Get("/containers", h.ListContainers)
				r.Get("/containers/summary", h.ContainerSummaryEndpoint)
				r.Get("/containers/{id}", h.InspectContainer)
				// Historical metrics are read-only data, not a control action.
				r.Get("/containers/{id}/metrics", h.GetMetrics)
				// File browser (read-only) — list dir + read text preview +
				// stream raw file bytes for download. Write goes through a
				// separate group keyed on containers.exec because uploading
				// a file is effectively code injection.
				r.Get("/containers/{id}/files", h.BrowseContainerFiles)
				r.Get("/containers/{id}/files/content", h.ReadContainerFile)
				r.Get("/containers/{id}/files/download", h.DownloadContainerFile)
				r.Get("/hosts/{id}/stats/containers", h.BatchContainerStats)
			})

			// File-write into a container is gated on containers.exec
			// because the blast radius is the same as a shell session.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermContainersExec))
				r.Post("/containers/{id}/files", h.WriteContainerFile)
			})

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermImagesView))
				r.Get("/images", h.ListImages)
				r.Get("/images/updates", h.ListImageUpdates)
				r.Get("/images/{id}", h.InspectImage)
			})

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermNetworksView))
				r.Get("/networks", h.ListNetworks)
				r.Get("/networks/topology", h.GetTopology)
				r.Get("/networks/{id}", h.InspectNetwork)
			})

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermVolumesView))
				r.Get("/volumes", h.ListVolumes)
				r.Get("/volumes/{name}", h.InspectVolume)
			})

			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemView))
				// Host-level CPU / RAM / disk snapshot for the dashboard.
				r.Get("/system/metrics", h.SystemMetrics)
				// Default-system-backup status for the sidebar pill.
				// Read-only — any authenticated viewer can see whether
				// the server is self-protected.
				r.Get("/system/backup-status", h.BackupStatus)
				r.Get("/system/health", h.SystemHealth)
				r.Get("/system/info", h.SystemInfo)
				r.Get("/system/update-status", h.GetUpdateStatus)
			})

			// -------------------------- STACK CREATE / UPDATE / DELETE -------
			// Split per RBAC v2.1: POST → stacks.create, PUT → stacks.update,
			// DELETE / discard / cleanup-preview → stacks.delete. The
			// recover endpoint rebuilds an existing stack's compose, so
			// it's stacks.update. convert/run-to-compose is a helper for
			// authoring new stacks → stacks.create.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStacksCreate))
				r.Post("/stacks", h.CreateStack)
				r.Post("/stacks/from-git", h.CreateStackFromGit)
				r.Post("/convert/run-to-compose", h.ConvertRunToCompose)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStacksUpdate))
				r.Put("/stacks/{name}", h.UpdateStack)
				r.Post("/stacks/{name}/recover", h.RecoverStack)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStacksDelete))
				r.Delete("/stacks/{name}", h.DeleteStack)
				r.Get("/stacks/{name}/cleanup-preview", h.CleanupPreview)
				r.Post("/stacks/{name}/discard", h.DiscardStack)
			})

			// -------------------------- STACK ADOPT --------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStacksAdopt))
				r.Post("/stacks/adopt", h.AdoptStack)
			})

			// Git source CRUD (P.11.11). Configuring a source writes
			// compose.yaml into the stack FS, so it rides on stacks.update.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStacksUpdate))
				r.Get("/stacks/{name}/git", h.GetGitSource)
				r.Post("/stacks/{name}/git", h.ConfigureGitSource)
				r.Delete("/stacks/{name}/git", h.DeleteGitSource)
				r.Post("/stacks/{name}/git/sync", h.SyncGitSource)
			})

			// Stack templates (P.11.12) — split create/update/delete +
			// deploy. Listing remains open below.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermTemplatesCreate))
				r.Post("/templates", h.CreateTemplate)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermTemplatesUpdate))
				r.Put("/templates/{id}", h.UpdateTemplate)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermTemplatesDelete))
				r.Delete("/templates/{id}", h.DeleteTemplate)
			})
			r.Group(func(r chi.Router) {
				// Deploying a template runs compose-up against a stack
				// — same risk class as stacks.deploy.
				r.Use(middleware.RequirePerm(rbac.PermStacksDeploy))
				r.Post("/templates/{id}/deploy", h.DeployTemplate)
			})

			// Templates — read-only endpoints available to any
			// authenticated user.
			r.Get("/templates", h.ListTemplates)
			r.Get("/templates/{id}", h.GetTemplate)
			r.Get("/templates/{id}/export", h.ExportTemplate)

			// -------------------------- STACK DEPLOY -------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStacksDeploy))
				r.Post("/stacks/{name}/deploy", h.DeployStack)
				r.Post("/stacks/{name}/stop", h.StopStack)
				// Service scaling (P.8)
				r.Get("/stacks/{name}/scale", h.ListServiceScale)
				r.Get("/stacks/{name}/services/{service}/scale", h.GetScale)
				r.Post("/stacks/{name}/services/{service}/scale", h.ScaleService)
				// Rolling updates (P.12.5b)
				r.Post("/stacks/{name}/services/{service}/rolling-update", h.RollingUpdateService)
				// Deploy history + rollback (P.12.6)
				r.Get("/stacks/{name}/deployments", h.ListDeployHistory)
				r.Get("/stacks/{name}/deployments/{id}", h.GetDeployHistoryEntry)
				r.Post("/stacks/{name}/deployments/{id}/rollback", h.RollbackToDeployment)
				// Stack dependencies (P.12.7)
				r.Get("/stacks/{name}/dependencies", h.GetStackDependencies)
				r.Put("/stacks/{name}/dependencies", h.SetStackDependencies)
				// Environment overrides (P.12.8)
				r.Get("/stacks/{name}/environments", h.ListEnvironments)
				r.Put("/stacks/{name}/environments/active", h.SetActiveEnvironment)
				// Auto-scaling rules (P.8)
				r.Get("/stacks/{name}/scaling-rules", h.GetScalingRules)
				r.Put("/stacks/{name}/scaling-rules", h.SetScalingRules)
				r.Delete("/stacks/{name}/scaling-rules", h.DeleteScalingRules)
			})

			// Migration (P.9) — separate sensitive perm because it
			// moves data between hosts.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStacksMigrate))
				r.Post("/stacks/{name}/migrate", h.InitiateMigration)
				r.Post("/stacks/{name}/migrate/preflight", h.PreflightMigration)
				r.Get("/stacks/{name}/migrate/{id}", h.GetMigration)
				r.Post("/stacks/{name}/migrate/{id}/rollback", h.RollbackMigration)
				r.Delete("/stacks/{name}/migrate/{id}/source", h.PurgeSource)
			})

			// Migrations global list (read-only, any authenticated user)
			r.Group(func(r chi.Router) {
				r.Get("/migrations", h.ListMigrations)
				r.Get("/migrations/active", h.ListActiveMigrations)
			})

			// System settings
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemUpdate))
				r.Get("/settings", h.ListSettings)
				r.Put("/settings", h.UpdateSettings)
			})

			// Global environment variables
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemUpdate))
				r.Get("/global-env", h.ListGlobalEnv)
				r.Post("/global-env", h.CreateGlobalEnv)
				r.Put("/global-env/{id}", h.UpdateGlobalEnv)
				r.Delete("/global-env/{id}", h.DeleteGlobalEnv)
				r.Get("/global-env/groups", h.ListGlobalEnvGroups)
				r.Get("/global-env/{id}/refs", h.GetGlobalEnvRefs)
			})

			// Roles RBAC v2 — split read / create / update / delete.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRolesView))
				r.Get("/roles", h.ListRoles)
				r.Get("/roles/permissions", h.AllPermissions)
				r.Get("/roles/{name}", h.GetRole)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRolesCreate))
				r.Post("/roles", h.CreateRole)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRolesUpdate))
				r.Put("/roles/{name}", h.UpdateRole)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRolesDelete))
				r.Delete("/roles/{name}", h.DeleteRole)
			})

			// API tokens — split into own (.view / .create / .delete) vs
			// cross-user (manage_others). Handlers themselves enforce the
			// own-vs-other rule + privilege-escalation guard on create
			// (caller may only mint tokens with a role ⊆ their perms).
			r.Group(func(r chi.Router) {
				r.With(middleware.RequirePerm(rbac.PermTokensView)).
					Get("/settings/api-tokens", h.ListAPITokens)
				r.With(middleware.RequirePerm(rbac.PermTokensCreate)).
					Post("/settings/api-tokens", h.CreateAPIToken)
				r.With(middleware.RequirePerm(rbac.PermTokensDelete)).
					Delete("/settings/api-tokens/{id}", h.RevokeAPIToken)
			})

			// Registry credentials (P.11.7) — split create / update /
			// delete. List is registries.view (any role with .view).
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRegistriesView))
				r.Get("/settings/registries", h.ListRegistries)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRegistriesCreate))
				r.Post("/settings/registries", h.CreateRegistry)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRegistriesUpdate))
				r.Put("/settings/registries/{id}", h.UpdateRegistry)
				r.Post("/settings/registries/{id}/test", h.TestRegistry)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRegistriesDelete))
				r.Delete("/settings/registries/{id}", h.DeleteRegistry)
			})

			// Host tags (P.11.2). Read for any authenticated user so
			// list pages can show tag chips; mutations gated to
			// hosts.tag.
			r.Group(func(r chi.Router) {
				r.Get("/hosts/tags/all", h.ListAllTags)
				r.Get("/hosts/{id}/tags", h.ListHostTags)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermHostsTag))
				r.Put("/hosts/{id}/tags", h.SetHostTags)
				r.Post("/hosts/{id}/tags", h.AddHostTag)
				r.Delete("/hosts/{id}/tags/{tag}", h.RemoveHostTag)
			})

			// Drain host (P.10) — host-level admin operation.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermHostsUpdate))
				r.Post("/hosts/{id}/drain/plan", h.PlanDrain)
				r.Post("/hosts/{id}/drain/execute", h.ExecuteDrain)
				r.Get("/hosts/{id}/drain/{drain_id}", h.GetDrain)
				r.Post("/hosts/{id}/drain/{drain_id}/pause", h.PauseDrain)
				r.Post("/hosts/{id}/drain/{drain_id}/resume", h.ResumeDrain)
				r.Post("/hosts/{id}/drain/{drain_id}/abort", h.AbortDrain)
			})

			// -------------------------- CONTAINER CONTROL --------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermContainersUpdate))
				r.Post("/containers/{id}/start", h.StartContainer)
				r.Post("/containers/{id}/stop", h.StopContainer)
				r.Post("/containers/{id}/restart", h.RestartContainer)
				r.Post("/containers/{id}/pause", h.PauseContainer)
				r.Post("/containers/{id}/unpause", h.UnpauseContainer)
				r.Post("/containers/{id}/kill", h.KillContainer)
				r.Delete("/containers/{id}", h.RemoveContainer)
				r.Get("/containers/{id}/update-info", h.PreviewUpdate)
				r.Post("/containers/{id}/update", h.UpdateContainer)
				r.Post("/containers/{id}/rollback", h.RollbackContainer)
				r.Get("/containers/{id}/update-history", h.UpdateHistory)
			})

			// -------------------------- IMAGES -------------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermImagesCreate))
				r.Post("/images/pull", h.PullImage)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermImagesDelete))
				r.Delete("/images/{id}", h.RemoveImage)
				r.Post("/images/prune", h.PruneImages)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermImagesScan))
				r.Post("/images/{id}/scan", h.ScanImage)
				r.Get("/images/{id}/scan", h.GetScan)
			})

			// -------------------------- NETWORKS -----------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermNetworksCreate))
				r.Post("/networks", h.CreateNetwork)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermNetworksDelete))
				r.Delete("/networks/{id}", h.RemoveNetwork)
				r.Post("/networks/prune", h.PruneNetworks)
			})

			// -------------------------- VOLUMES ------------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermVolumesCreate))
				r.Post("/volumes", h.CreateVolume)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermVolumesDelete))
				r.Delete("/volumes/{name}", h.RemoveVolume)
				r.Post("/volumes/prune", h.PruneVolumes)
			})

			// Volume content browsing (P.11.8). volumes.browse is the
			// dedicated sensitive perm — read access to volume data is
			// PII-class. Every call is also audited by the handler.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermVolumesBrowse))
				r.Get("/volumes/{name}/browse", h.BrowseVolume)
				r.Get("/volumes/{name}/browse/file", h.ReadVolumeFile)
			})

			// -------------------------- DISASTER RECOVERY --------------------
			// Backup verification (P.12.4) — destructive precursor to
			// a real restore, so backups.restore is the right gate.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermBackupsRestore))
				r.Post("/restore/verify", h.VerifyUploadedBackup)
				r.Post("/backups/runs/{id}/verify", h.VerifyBackupRun)
			})

			// -------------------------- USERS --------------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermUsersView))
				r.Get("/users", h.ListUsers)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermUsersCreate))
				r.Post("/users", h.CreateUser)
				// Invite-link flow: admin generates a one-time URL,
				// shares it manually. Recipient hits the public
				// /invite/{token} endpoints below to redeem.
				r.Get("/invites", h.ListInvites)
				r.Post("/invites", h.CreateInvite)
				r.Delete("/invites/{id}", h.RevokeInvite)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermUsersUpdate))
				r.Put("/users/{id}", h.UpdateUser)
				// P.12.1 — admin-only account unlock.
				r.Post("/users/{id}/unlock", h.UnlockUser)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermUsersDelete))
				r.Delete("/users/{id}", h.DeleteUser)
				r.Delete("/users/{id}/mfa", h.MFAReset)
			})

			// Password policy is global system config (not per-user) —
			// gate behind system.update.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemUpdate))
				r.Get("/auth/policy", h.GetPasswordPolicy)
				r.Put("/auth/policy", h.UpdatePasswordPolicy)
				r.Get("/auth/signin", h.GetSignInConfig)
				r.Put("/auth/signin", h.UpdateSignInConfig)
			})

			// -------------------------- AUDIT READ ---------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermAuditView))
				r.Get("/audit", h.ListAudit)
				r.Get("/audit/verify", h.VerifyAudit)
				r.Get("/audit/retention", h.GetAuditRetention)
			})

			// Audit retention config + manual-run (P.11.13) + webhook
			// (P.11.14). audit.write is the dedicated gate so a
			// read-only auditor can't tamper with the compliance trail
			// (closes the loophole called out in the catalog audit memo).
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermAuditWrite))
				r.Put("/audit/retention", h.UpdateAuditRetention)
				r.Post("/audit/retention/run", h.RunAuditRetention)
				r.Get("/audit/webhook", h.GetAuditWebhook)
				r.Put("/audit/webhook", h.UpdateAuditWebhook)
				r.Post("/audit/webhook/test", h.TestAuditWebhook)
			})

			// -------------------------- UNIFIED PROVIDERS --------------------
			// Frontend can fetch all four kinds in one round-trip instead
			// of paging through /oidc/providers + /oauth2/providers + …
			// Read-only — mutations still go to the per-kind endpoints
			// because each kind has a different config shape.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemUpdate))
				r.Get("/auth/providers", h.ListAuthProviders)
			})

			// -------------------------- OIDC ADMIN ---------------------------
			// Identity-provider configuration is system-level admin —
			// gated by system.update until we add a dedicated auth.config
			// permission in the post-v0.3 SSO slice.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemUpdate))
				r.Get("/oidc/providers", h.ListOIDCProviders)
				r.Post("/oidc/providers", h.CreateOIDCProvider)
				r.Put("/oidc/providers/{id}", h.UpdateOIDCProvider)
				r.Delete("/oidc/providers/{id}", h.DeleteOIDCProvider)
				r.Post("/oidc/providers/reload", h.ReloadOIDCProviders)
			r.Post("/oidc/providers/test-discovery", h.TestOIDCDiscovery)
			r.Post("/oidc/providers/{id}/test", h.TestOIDCProvider)
			})

			// -------------------------- SAML ADMIN ---------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemUpdate))
				r.Get("/saml/providers", h.ListSAMLProviders)
				r.Get("/saml/providers/{id}", h.GetSAMLProvider)
				r.Post("/saml/providers", h.CreateSAMLProvider)
				r.Put("/saml/providers/{id}", h.UpdateSAMLProvider)
				r.Delete("/saml/providers/{id}", h.DeleteSAMLProvider)
				r.Post("/saml/providers/{id}/test", h.TestSAMLProvider)
			})

			// -------------------------- LDAP ADMIN ---------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemUpdate))
				r.Get("/ldap/providers", h.ListLDAPProviders)
				r.Get("/ldap/providers/{id}", h.GetLDAPProvider)
				r.Post("/ldap/providers", h.CreateLDAPProvider)
				r.Put("/ldap/providers/{id}", h.UpdateLDAPProvider)
				r.Delete("/ldap/providers/{id}", h.DeleteLDAPProvider)
				r.Post("/ldap/providers/{id}/test", h.TestLDAPProvider)
			})

			// -------------------------- OAUTH2 ADMIN -------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermSystemUpdate))
				r.Get("/oauth2/providers", h.ListOAuth2Providers)
				r.Get("/oauth2/providers/{id}", h.GetOAuth2Provider)
				r.Post("/oauth2/providers", h.CreateOAuth2Provider)
				r.Put("/oauth2/providers/{id}", h.UpdateOAuth2Provider)
				r.Delete("/oauth2/providers/{id}", h.DeleteOAuth2Provider)
				r.Post("/oauth2/providers/{id}/test", h.TestOAuth2Provider)
			})

			// -------------------------- ALERTS -------------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermAlertsView))
				r.Get("/notifications/channels", h.ListNotificationChannels)
				r.Get("/alerts/rules", h.ListAlertRules)
				r.Get("/alerts/rules/{id}/stats", h.GetAlertRuleStats)
				r.Get("/alerts/history", h.ListAlertHistory)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermAlertsCreate))
				r.Post("/notifications/channels", h.CreateNotificationChannel)
				r.Post("/alerts/rules", h.CreateAlertRule)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermAlertsUpdate))
				r.Put("/notifications/channels/{id}", h.UpdateNotificationChannel)
				r.Post("/notifications/channels/{id}/test", h.TestNotificationChannel)
				r.Put("/alerts/rules/{id}", h.UpdateAlertRule)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermAlertsDelete))
				r.Delete("/notifications/channels/{id}", h.DeleteNotificationChannel)
				r.Delete("/alerts/rules/{id}", h.DeleteAlertRule)
			})

			// -------------------------- AGENTS (admin) -----------------------
			// "Agents" is the technical name for the binary that runs on
			// remote hosts; the user-facing concept is "host". hosts.update
			// gates lifecycle (drain/upgrade/rotate); hosts.delete gates
			// revoke; ListAgents is admin-shaped for now (acts like a
			// hosts admin view), so all routes ride on hosts.update.
			// "Agents" is the technical name for the binary that runs on
			// remote hosts; the user-facing concept is "host". POST /agents
			// = enroll a new host (hosts.create); upgrade/rotate/drain
			// touch existing hosts (hosts.update); DELETE = revoke
			// (hosts.delete); GETs = hosts.view.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermHostsView))
				r.Get("/agents", h.ListAgents)
				r.Get("/agents/{id}", h.GetAgent)
				r.Get("/agents/upgrade-policy", h.GetAgentUpgradePolicy)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermHostsCreate))
				r.Post("/agents", h.CreateAgent)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermHostsUpdate))
				r.Post("/agents/{id}/upgrade", h.UpgradeAgent)
				r.Post("/agents/{id}/rotate-token", h.RotateAgentEnrollToken)
				r.Put("/agents/upgrade-policy", h.UpdateAgentUpgradePolicy)
				r.Post("/agents/upgrade-policy/run", h.RunAgentUpgradeEvaluation)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermHostsDelete))
				r.Delete("/agents/{id}", h.DeleteAgent)
			})

			// -------------------------- BACKUPS ------------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermBackupsView))
				r.Get("/backups/jobs", h.ListBackupJobs)
				r.Get("/backups/jobs/{id}", h.GetBackupJob)
				r.Get("/backups/targets", h.ListBackupTargets)
				r.Get("/backups/runs", h.ListBackupRuns)
				r.Get("/backups/runs/{id}", h.GetBackupRun)
				r.Get("/backups/runs/{id}/log", h.GetBackupRunLog)
				// Archive download is gated by backups.view: anyone
				// who can list runs can also pull the bytes for one
				// they own. Restore-grade access stays under
				// backups.restore.
				r.Get("/backups/runs/{id}/archive", h.DownloadBackupArchive)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermBackupsCreate))
				r.Post("/backups/jobs", h.CreateBackupJob)
				r.Post("/backups/targets", h.CreateBackupTarget)
				r.Post("/backups/targets/test-config", h.TestBackupTargetConfig)
				r.Post("/backups/targets/discover-shares", h.DiscoverSMBShares)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermBackupsUpdate))
				r.Put("/backups/jobs/{id}", h.UpdateBackupJob)
				r.Post("/backups/jobs/{id}/run", h.RunBackupJob)
				r.Post("/backups/jobs/{id}/review/{mode}", h.AcknowledgeBackupJobReview)
				r.Put("/backups/targets/{id}", h.UpdateBackupTarget)
				r.Post("/backups/targets/{id}/test", h.TestBackupTarget)
				r.Put("/backups/system/enabled", h.SetBackupEnabled)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermBackupsDelete))
				r.Delete("/backups/jobs/{id}", h.DeleteBackupJob)
				r.Delete("/backups/targets/{id}", h.DeleteBackupTarget)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermBackupsRestore))
				r.Post("/backups/runs/{id}/restore", h.RestoreBackup)
				// P.13.3: stack-typed runs need their own entry point so
				// the restore actually writes stack/<rel> + each volume
				// instead of returning the legacy "not implemented" stub.
				r.Post("/backups/runs/{id}/restore-stack", h.RestoreStackBackup)
				// Backup-key export is sensitive material that lets you
				// decrypt every backup blob — gate behind backups.restore.
				r.Get("/system/backup-key/export", h.ExportBackupKey)
			})
			r.Group(func(r chi.Router) {
				// Server-binary self-update + secrets-key rotation are the
				// most destructive operations in the platform.
				r.Use(middleware.RequirePerm(rbac.PermSystemUpgrade))
				r.Post("/system/secrets/rotate", h.RotateSecretsKey)
				r.Post("/system/update-check", h.CheckUpdateNow)
			})

			// -------------------------- PROXY -------------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermProxyView))
				r.Get("/proxy/status", h.ProxyStatus)
				r.Get("/proxy/routes", h.ListProxyRoutes)
				r.Get("/proxy/routes/{id}/metrics", h.GetProxyRouteMetrics)
				r.Get("/proxy/metrics", h.GetProxyMetrics)
				r.Get("/proxy/acme-events", h.ListACMEEvents)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermProxyCreate))
				r.Post("/proxy/routes", h.CreateProxyRoute)
				// Enable/disable the proxy itself is closer to "create
				// the proxy stack" than route-level edit.
				r.Post("/proxy/enable", h.ProxyEnable)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermProxyUpdate))
				r.Put("/proxy/routes/{id}", h.UpdateProxyRoute)
				r.Patch("/proxy/routes/{id}/enabled", h.SetProxyRouteEnabled)
			})
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermProxyDelete))
				r.Delete("/proxy/routes/{id}", h.DeleteProxyRoute)
				r.Post("/proxy/disable", h.ProxyDisable)
			})
		})

		// WebSocket endpoints — auth via ?ticket= (not Bearer header).
		// Tickets are bound to one permission at issue time
		// (POST /ws/ticket?for=<perm>). Each handler asserts
		// claims.Perm == required so a logs-ticket can't reach /ws/exec.
		r.Get("/ws/logs/{id}", h.WSLogs)
		r.Get("/ws/events", h.WSEvents)
		r.Get("/ws/exec/{id}", h.WSExec)
		r.Get("/ws/stats/{id}", h.WSStats)
	})

	// Public installer + binary download. Lives outside /api/v1 because
	// they're file downloads, not REST endpoints. The token in the script
	// URL is the auth (re-validated on enroll). The binary is unauthenticated
	// because it's just public code.
	r.Get("/install/agent.sh", h.AgentInstallScript)
	r.Get("/install/{name}", h.AgentBinary)

	if webFS != nil {
		r.Handle("/*", spaHandler(webFS))
	}

	return r
}

// spaHandler serves files from the embedded SvelteKit build with a single-page
// app fallback: any request for a path that doesn't resolve to a file falls
// back to index.html so client-side routes (e.g. /backups, /containers/abc)
// keep working on full-page reloads.
func spaHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never fall back for API or WS paths — those are handled above. If
		// chi reaches the file handler with /api/* it means the route is
		// genuinely missing and a 404 is correct.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" {
			clean = "index.html"
		}
		if f, err := webFS.Open(clean); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Unknown path → serve index.html so the SvelteKit router takes over.
		index, err := webFS.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer index.Close()
		stat, err := index.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", stat.ModTime(), index.(io.ReadSeeker))
	})
}
