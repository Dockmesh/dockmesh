package handlers

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/settings"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/dockmesh/dockmesh/internal/oidc"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
	"github.com/dockmesh/dockmesh/internal/registries"
	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/templates"
	"github.com/dockmesh/dockmesh/internal/updater"
)

type Handlers struct {
	DB           *sql.DB
	Auth         *auth.Service
	Audit        *audit.Service
	Docker       *docker.Client // may be nil if the daemon was unreachable at startup
	Stacks       *stacks.Manager
	Deployments  *stacks.DeploymentStore
	DeployHistory *stacks.HistoryStore
	Dependencies  *stacks.DependencyStore
	Compose      *compose.Service
	LoginLimiter *ratelimit.Limiter
	Scanner      scanner.Scanner
	ScanStore    *scanner.Store
	Proxy        *proxy.Service
	Updater      *updater.Service
	OIDC         *oidc.Service
	Metrics      *metrics.Collector
	Notify       *notify.Service
	Alerts       *alerts.Service
	Backups        *backup.Service
	BackupTargets  *targets.TargetStore
	Migrations     *migration.Service
	Drains         *migration.DrainService
	Agents         *agents.Service
	Hosts          *host.Registry
	HostTags       *hosttags.Service
	Roles          *rbac.Store
	Settings       *settings.Store
	GlobalEnv      *globalenv.Store
	APITokens      *apitokens.Service
	Registries     *registries.Service
	GitSource      *gitsource.Service
	Templates      *templates.Service
	AuditRetention *audit.Retention
	AuditWebhook   *audit.Webhook
	AgentUpgrade   *agents.UpgradeController
	Prom           *metrics.PromMetrics
	JWTSecret      []byte // raw secret used to sign the short-lived OIDC state cookie
}

type Deps struct {
	DB           *sql.DB
	Auth         *auth.Service
	Audit        *audit.Service
	Docker       *docker.Client
	Stacks       *stacks.Manager
	Deployments  *stacks.DeploymentStore
	DeployHistory *stacks.HistoryStore
	Dependencies  *stacks.DependencyStore
	Compose      *compose.Service
	LoginLimiter *ratelimit.Limiter
	Scanner      scanner.Scanner
	ScanStore    *scanner.Store
	Proxy        *proxy.Service
	Updater      *updater.Service
	OIDC         *oidc.Service
	Metrics      *metrics.Collector
	Notify       *notify.Service
	Alerts       *alerts.Service
	Backups        *backup.Service
	BackupTargets  *targets.TargetStore
	Migrations     *migration.Service
	Drains         *migration.DrainService
	Agents         *agents.Service
	Hosts          *host.Registry
	HostTags       *hosttags.Service
	Roles          *rbac.Store
	Settings       *settings.Store
	GlobalEnv      *globalenv.Store
	APITokens      *apitokens.Service
	Registries     *registries.Service
	GitSource      *gitsource.Service
	Templates      *templates.Service
	AuditRetention *audit.Retention
	AuditWebhook   *audit.Webhook
	AgentUpgrade   *agents.UpgradeController
	Prom           *metrics.PromMetrics
	JWTSecret      []byte
}

func New(d Deps) *Handlers {
	return &Handlers{
		DB:          d.DB,
		Auth:        d.Auth,
		Audit:       d.Audit,
		Docker:      d.Docker,
		Stacks:      d.Stacks,
		Deployments: d.Deployments,
		DeployHistory: d.DeployHistory,
		Dependencies: d.Dependencies,
		Compose:     d.Compose,
		LoginLimiter: d.LoginLimiter,
		Scanner:     d.Scanner,
		ScanStore:   d.ScanStore,
		Proxy:       d.Proxy,
		Updater:     d.Updater,
		OIDC:        d.OIDC,
		Metrics:     d.Metrics,
		Notify:      d.Notify,
		Alerts:      d.Alerts,
		Backups:       d.Backups,
		BackupTargets: d.BackupTargets,
		Migrations:    d.Migrations,
		Drains:      d.Drains,
		Agents:      d.Agents,
		Hosts:       d.Hosts,
		HostTags:    d.HostTags,
		Roles:       d.Roles,
		Settings:    d.Settings,
		GlobalEnv:   d.GlobalEnv,
		APITokens:   d.APITokens,
		Registries:  d.Registries,
		GitSource:      d.GitSource,
		Templates:      d.Templates,
		AuditRetention: d.AuditRetention,
		AuditWebhook:   d.AuditWebhook,
		AgentUpgrade:   d.AgentUpgrade,
		Prom:           d.Prom,
		JWTSecret:   d.JWTSecret,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("encode json", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, dst any) error {
	// Permissive: unknown fields are silently ignored so forward-compat
	// clients (sending optional/future fields) don't get 400 "invalid body".
	// Individual handlers that need strict validation can still enforce it
	// on the decoded struct.
	dec := json.NewDecoder(r.Body)
	return dec.Decode(dst)
}

// pickHost resolves the ?host=<id> query parameter against the host
// registry. Empty / missing / "local" returns the local docker daemon.
// Unknown / offline agents bubble up as ErrAgentOffline / ErrUnknownHost
// so the caller can return 503.
func (h *Handlers) pickHost(r *http.Request) (host.Host, error) {
	id := r.URL.Query().Get("host")
	if h.Hosts == nil {
		// Backwards compat: fall back to wrapping the docker.Client directly
		// so a server started without a registry still works.
		if h.Docker == nil {
			return nil, host.ErrNoDocker
		}
		return host.NewLocal(h.Docker), nil
	}
	return h.Hosts.Pick(id)
}
