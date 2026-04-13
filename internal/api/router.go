package api

import (
	"io/fs"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func NewRouter(h *handlers.Handlers, authSvc *auth.Service, webFS fs.FS) http.Handler {
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

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", h.Health)

		r.Post("/auth/login", h.Login)
		r.Post("/auth/mfa", h.LoginMFA)
		r.Post("/auth/logout", h.Logout)
		r.Post("/auth/refresh", h.Refresh)

		// OIDC flow is public — state is carried in a signed cookie.
		r.Get("/auth/oidc/providers", h.ListOIDCProvidersPublic)
		r.Get("/auth/oidc/{slug}/login", h.OIDCLogin)
		r.Get("/auth/oidc/{slug}/callback", h.OIDCCallback)

		r.Group(func(r chi.Router) {
			r.Use(middleware.NewAuth(authSvc))

			// Self-service routes (any authenticated user)
			r.Get("/me", h.Me)
			r.Put("/users/{id}/password", h.ChangeUserPassword) // self or admin (enforced inside)
			r.Post("/ws/ticket", h.WSTicket)

			// Self MFA enrollment / disable
			r.Post("/mfa/enroll/start", h.MFAEnrollStart)
			r.Post("/mfa/enroll/verify", h.MFAEnrollVerify)
			r.Delete("/mfa", h.MFADisable)

			// -------------------------- READ ROUTES --------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermRead))

				r.Get("/stacks", h.ListStacks)
				r.Get("/stacks/{name}", h.GetStack)
				r.Get("/stacks/{name}/status", h.StackStatus)

				r.Get("/containers", h.ListContainers)
				r.Get("/containers/{id}", h.InspectContainer)

				r.Get("/images", h.ListImages)

				r.Get("/networks", h.ListNetworks)
				r.Get("/networks/{id}", h.InspectNetwork)

				r.Get("/volumes", h.ListVolumes)
				r.Get("/volumes/{name}", h.InspectVolume)
			})

			// -------------------------- STACK WRITE --------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStackWrite))
				r.Post("/stacks", h.CreateStack)
				r.Put("/stacks/{name}", h.UpdateStack)
				r.Delete("/stacks/{name}", h.DeleteStack)
				r.Post("/convert/run-to-compose", h.ConvertRunToCompose)
			})

			// -------------------------- STACK DEPLOY -------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermStackDeploy))
				r.Post("/stacks/{name}/deploy", h.DeployStack)
				r.Post("/stacks/{name}/stop", h.StopStack)
			})

			// -------------------------- CONTAINER CONTROL --------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermContainerControl))
				r.Post("/containers/{id}/start", h.StartContainer)
				r.Post("/containers/{id}/stop", h.StopContainer)
				r.Post("/containers/{id}/restart", h.RestartContainer)
				r.Delete("/containers/{id}", h.RemoveContainer)
				r.Get("/containers/{id}/update-info", h.PreviewUpdate)
				r.Post("/containers/{id}/update", h.UpdateContainer)
				r.Post("/containers/{id}/rollback", h.RollbackContainer)
				r.Get("/containers/{id}/update-history", h.UpdateHistory)

				r.Get("/containers/{id}/metrics", h.GetMetrics)
			})

			// -------------------------- IMAGE WRITE --------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermImageWrite))
				r.Post("/images/pull", h.PullImage)
				r.Delete("/images/{id}", h.RemoveImage)
				r.Post("/images/prune", h.PruneImages)
			})

			// -------------------------- IMAGE SCAN ---------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermImageScan))
				r.Post("/images/{id}/scan", h.ScanImage)
				r.Get("/images/{id}/scan", h.GetScan)
			})

			// -------------------------- NETWORK WRITE ------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermNetworkWrite))
				r.Post("/networks", h.CreateNetwork)
				r.Delete("/networks/{id}", h.RemoveNetwork)
			})

			// -------------------------- VOLUME WRITE -------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermVolumeWrite))
				r.Post("/volumes", h.CreateVolume)
				r.Delete("/volumes/{name}", h.RemoveVolume)
				r.Post("/volumes/prune", h.PruneVolumes)
			})

			// -------------------------- USER MANAGE --------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermUserManage))
				r.Get("/users", h.ListUsers)
				r.Post("/users", h.CreateUser)
				r.Put("/users/{id}", h.UpdateUser)
				r.Delete("/users/{id}", h.DeleteUser)
				r.Delete("/users/{id}/mfa", h.MFAReset)
			})

			// -------------------------- AUDIT READ ---------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermAuditRead))
				r.Get("/audit", h.ListAudit)
				r.Get("/audit/verify", h.VerifyAudit)
			})

			// -------------------------- OIDC ADMIN ---------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermUserManage))
				r.Get("/oidc/providers", h.ListOIDCProviders)
				r.Post("/oidc/providers", h.CreateOIDCProvider)
				r.Put("/oidc/providers/{id}", h.UpdateOIDCProvider)
				r.Delete("/oidc/providers/{id}", h.DeleteOIDCProvider)
			})

			// -------------------------- ALERTS (admin) -----------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermUserManage))
				r.Get("/notifications/channels", h.ListNotificationChannels)
				r.Post("/notifications/channels", h.CreateNotificationChannel)
				r.Put("/notifications/channels/{id}", h.UpdateNotificationChannel)
				r.Delete("/notifications/channels/{id}", h.DeleteNotificationChannel)
				r.Post("/notifications/channels/{id}/test", h.TestNotificationChannel)

				r.Get("/alerts/rules", h.ListAlertRules)
				r.Post("/alerts/rules", h.CreateAlertRule)
				r.Put("/alerts/rules/{id}", h.UpdateAlertRule)
				r.Delete("/alerts/rules/{id}", h.DeleteAlertRule)
				r.Get("/alerts/history", h.ListAlertHistory)
			})

			// -------------------------- PROXY (admin) ------------------------
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePerm(rbac.PermUserManage))
				r.Get("/proxy/status", h.ProxyStatus)
				r.Post("/proxy/enable", h.ProxyEnable)
				r.Post("/proxy/disable", h.ProxyDisable)
				r.Get("/proxy/routes", h.ListProxyRoutes)
				r.Post("/proxy/routes", h.CreateProxyRoute)
				r.Put("/proxy/routes/{id}", h.UpdateProxyRoute)
				r.Delete("/proxy/routes/{id}", h.DeleteProxyRoute)
			})
		})

		// WebSocket endpoints — auth via ?ticket= (not Bearer header).
		// Ticket issuance already goes through RequirePerm(PermRead) on
		// /ws/ticket — we trust tickets once issued. Future: encode the
		// target perm into the ticket itself.
		r.Get("/ws/logs/{id}", h.WSLogs)
		r.Get("/ws/events", h.WSEvents)
		r.Get("/ws/exec/{id}", h.WSExec)
		r.Get("/ws/stats/{id}", h.WSStats)
	})

	if webFS != nil {
		r.Handle("/*", http.FileServer(http.FS(webFS)))
	}

	return r
}
