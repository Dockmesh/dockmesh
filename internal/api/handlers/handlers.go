package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/ldapauth"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/settings"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/changelog"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/deploy"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/dockmesh/dockmesh/internal/oauth2auth"
	"github.com/dockmesh/dockmesh/internal/oidc"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/saml"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
	"github.com/dockmesh/dockmesh/internal/registries"
	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
	"github.com/dockmesh/dockmesh/internal/setup"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/templates"
	"github.com/dockmesh/dockmesh/internal/updater"
	"github.com/dockmesh/dockmesh/internal/userprefs"
	"github.com/dockmesh/dockmesh/internal/invites"
)

type Handlers struct {
	DB           *sql.DB
	Auth         *auth.Service
	Audit        *audit.Service
	Docker       *docker.Client // may be nil if the daemon was unreachable at startup
	Stacks       *stacks.Manager
	Deployments  *stacks.DeploymentStore
	DeployHistory *stacks.HistoryStore
	DeployTracker *deploy.Tracker
	Notifications *notifications.Service
	Notices       *notices.Service
	Dependencies  *stacks.DependencyStore
	Compose      *compose.Service
	LoginLimiter *ratelimit.Limiter
	// mfaDisableLimiter caps wrong codes on DELETE /mfa per user.
	mfaDisableLimiter *ratelimit.Limiter
	mfaLimiter        *ratelimit.Limiter // per IP; per account in auth.Service
	ldapLimits        authLimits
	inviteLimits      authLimits
	refreshLimits     authLimits
	Scanner      scanner.Scanner
	ScanStore    *scanner.Store
	Proxy        *proxy.Service
	Updater      *updater.Service
	UpdateWatcher *updater.Watcher
	OIDC         *oidc.Service
	SAML         *saml.Service
	LDAP         *ldapauth.Service
	OAuth2       *oauth2auth.Service
	Metrics      *metrics.Collector
	Notify       *notify.Service
	Alerts       *alerts.Service
	Backups        *backup.Service
	BackupTargets  *targets.TargetStore
	Secrets        *secrets.Service
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
	Invites        *invites.Service
	AuditRetention *audit.Retention
	AuditWebhook   *audit.Webhook
	AgentUpgrade   *agents.UpgradeController
	Prom           *metrics.PromMetrics
	SelfUpdate     *selfupdate.Checker
	UserPrefs      *userprefs.Store
	SetupState     *setup.State    // first-run wizard state; nil = setup never active
	SetupCommit_   setup.CommitFunc // wizard's commit-runner — wired by main; nil = wizard cannot commit
	ssoCodes       ssoCodes
	setupRuns      sync.Map        // runID → *setup.Runner; populated when SetupCommit fires
	// RBACv2Enforce gates per-host + per-stack scope checks on typed-
	// resource handlers. Wired from cfg.RBACv2Enforce.
	RBACv2Enforce bool

	// ClientIPs resolves client addresses behind trusted proxies; the
	// router mounts its middleware.
	ClientIPs   *clientip.Resolver
	proxyNotice *proxyNotice
	// releaseSections is the embedded changelog, parsed once.
	releaseSections []*changelog.Section
	// gitWebhookTrigger replaces GitSource.TriggerSync in tests.
	gitWebhookTrigger func(ctx context.Context, stackName string)
}

type Deps struct {
	DB           *sql.DB
	Auth         *auth.Service
	Audit        *audit.Service
	Docker       *docker.Client
	Stacks       *stacks.Manager
	Deployments  *stacks.DeploymentStore
	DeployHistory *stacks.HistoryStore
	DeployTracker *deploy.Tracker
	Notifications *notifications.Service
	Notices       *notices.Service
	Dependencies  *stacks.DependencyStore
	Compose      *compose.Service
	LoginLimiter *ratelimit.Limiter
	Scanner      scanner.Scanner
	ScanStore    *scanner.Store
	Proxy        *proxy.Service
	Updater      *updater.Service
	UpdateWatcher *updater.Watcher
	OIDC         *oidc.Service
	SAML         *saml.Service
	LDAP         *ldapauth.Service
	OAuth2       *oauth2auth.Service
	Metrics      *metrics.Collector
	Notify       *notify.Service
	Alerts       *alerts.Service
	Backups        *backup.Service
	BackupTargets  *targets.TargetStore
	Secrets        *secrets.Service
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
	Invites        *invites.Service
	AuditRetention *audit.Retention
	AuditWebhook   *audit.Webhook
	AgentUpgrade   *agents.UpgradeController
	Prom           *metrics.PromMetrics
	SelfUpdate     *selfupdate.Checker
	UserPrefs      *userprefs.Store
	// Changelog is CHANGELOG.md as embedded in the binary.
	Changelog      string
	SetupState     *setup.State
	SetupCommit_   setup.CommitFunc
	RBACv2Enforce  bool
}

func New(d Deps) *Handlers {
	h := &Handlers{
		DB:          d.DB,
		Auth:        d.Auth,
		Audit:       d.Audit,
		Docker:      d.Docker,
		Stacks:      d.Stacks,
		Deployments: d.Deployments,
		DeployHistory: d.DeployHistory,
		DeployTracker: d.DeployTracker,
		Notifications: d.Notifications,
		Notices:       d.Notices,
		Dependencies: d.Dependencies,
		Compose:     d.Compose,
		LoginLimiter: d.LoginLimiter,
		Scanner:     d.Scanner,
		ScanStore:   d.ScanStore,
		Proxy:       d.Proxy,
		Updater:     d.Updater,
		UpdateWatcher: d.UpdateWatcher,
		OIDC:        d.OIDC,
		SAML:        d.SAML,
		LDAP:        d.LDAP,
		OAuth2:      d.OAuth2,
		Metrics:     d.Metrics,
		Notify:      d.Notify,
		Alerts:      d.Alerts,
		Backups:       d.Backups,
		BackupTargets: d.BackupTargets,
		Secrets:       d.Secrets,
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
		Invites:        d.Invites,
		AuditRetention: d.AuditRetention,
		AuditWebhook:   d.AuditWebhook,
		AgentUpgrade:   d.AgentUpgrade,
		Prom:           d.Prom,
		SelfUpdate:     d.SelfUpdate,
		UserPrefs:      d.UserPrefs,
		SetupState:     d.SetupState,
		SetupCommit_:   d.SetupCommit_,
		RBACv2Enforce:  d.RBACv2Enforce,

		mfaDisableLimiter: ratelimit.New(5, 15*time.Minute, 15*time.Minute),
		mfaLimiter:        ratelimit.New(20, 15*time.Minute, 15*time.Minute),
		ldapLimits:        newAuthLimits(10, 10, 15*time.Minute),
		inviteLimits:      newAuthLimits(10, 5, 15*time.Minute),
		refreshLimits:     newAuthLimits(300, 0, 5*time.Minute),
		proxyNotice:       &proxyNotice{notices: d.Notices},
	}
	h.ClientIPs = clientip.New(func() string {
		if d.Settings == nil {
			return ""
		}
		return d.Settings.Get(settings.KeyTrustedProxies, "")
	})
	h.ClientIPs.OnUntrustedForward = h.proxyNotice.observe
	if d.Changelog != "" {
		sections, err := changelog.Parse(d.Changelog)
		if err != nil {
			slog.Warn("embedded changelog unreadable; what's new stays empty", "err", err)
		}
		h.releaseSections = sections
	}
	return h
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
