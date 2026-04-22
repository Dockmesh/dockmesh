package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"net/url"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/api"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/dockmesh/dockmesh/internal/oidc"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/scaling"
	"github.com/dockmesh/dockmesh/internal/settings"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
	"github.com/dockmesh/dockmesh/internal/registries"
	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/system"
	"github.com/dockmesh/dockmesh/internal/telemetry"
	"github.com/dockmesh/dockmesh/internal/templates"
	"github.com/dockmesh/dockmesh/internal/updater"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"github.com/dockmesh/dockmesh/pkg/version"
)

//go:embed all:web_dist
var webDist embed.FS

// loadEnvFileFromArgs scans os.Args for --env-file=PATH or
// --env-file PATH, loads that file as KEY=VALUE env vars (skipping
// `#`-prefixed comments + blank lines), and strips the flag out of
// os.Args so the rest of the process never sees it.
//
// This exists because launchd on macOS has no EnvironmentFile directive
// (unlike systemd), so init's plist template passes `--env-file PATH`
// to `dockmesh serve` explicitly. On Linux the flag is unused because
// systemd already injected the env before exec().
func loadEnvFileFromArgs() error {
	var path string
	var keep []string
	i := 0
	for i < len(os.Args) {
		arg := os.Args[i]
		if arg == "--env-file" {
			if i+1 >= len(os.Args) {
				return fmt.Errorf("--env-file: missing path argument")
			}
			path = os.Args[i+1]
			i += 2
			continue
		}
		if strings.HasPrefix(arg, "--env-file=") {
			path = strings.TrimPrefix(arg, "--env-file=")
			i++
			continue
		}
		keep = append(keep, arg)
		i++
	}
	if path == "" {
		return nil
	}
	os.Args = keep
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		// Strip matching surrounding quotes if present.
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		// Don't clobber vars the user already set in the environment
		// directly — that precedence matches systemd's behaviour.
		if _, already := os.LookupEnv(k); !already {
			_ = os.Setenv(k, v)
		}
	}
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Load --env-file if passed anywhere in the args. systemd handles
	// this natively via EnvironmentFile= in the unit, but launchd on
	// macOS has no equivalent so we parse the file ourselves and call
	// os.Setenv for each KEY=VALUE line. Removes the --env-file
	// argument from os.Args so downstream flag parsing doesn't choke.
	if err := loadEnvFileFromArgs(); err != nil {
		fmt.Fprintln(os.Stderr, "env-file:", err)
		os.Exit(1)
	}

	// Subcommand dispatch (§15.2, P.11.6 admin CLI suite).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			// fall through to server startup
		case "secrets":
			runSecretsCmd(os.Args[2:])
			return
		case "admin":
			runAdminCmd(os.Args[2:])
			return
		case "db":
			runDBCmd(os.Args[2:])
			return
		case "ca":
			runCACmd(os.Args[2:])
			return
		case "enroll":
			runEnrollCmd(os.Args[2:])
			return
		case "config":
			runConfigCmd(os.Args[2:])
			return
		case "doctor":
			runDoctorCmd(os.Args[2:])
			return
		case "completion":
			runCompletionCmd(os.Args[2:])
			return
		case "import":
			runImportCmd(os.Args[2:])
			return
		case "restore":
			runRestoreCmd(os.Args[2:])
			return
		case "init":
			runInitCmd(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Printf("dockmesh %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
			return
		case "help", "--help", "-h":
			printRootHelp()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", os.Args[1])
			printRootHelp()
			os.Exit(2)
		}
	}

	slog.Info("starting dockmesh", "version", version.Version, "commit", version.Commit)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	// P.12.3 — reconfigure slog from config once we have it. The
	// bootstrap logger above stays as-is; this replaces it so the
	// rest of the process runs at the configured level / format.
	slog.SetDefault(buildLogger(cfg.LogFormat, cfg.LogLevel))

	// OTel tracing (optional — off when DOCKMESH_OTEL_ENDPOINT is
	// empty). Init installs the global TracerProvider; otelhttp
	// middleware below opens a span per request.
	otelShutdown, err := telemetry.Init(context.Background(), telemetry.Config{
		Endpoint:       cfg.OTelEndpoint,
		Insecure:       cfg.OTelInsecure,
		ServiceName:    "dockmesh",
		ServiceVersion: version.Version,
	})
	if err != nil {
		slog.Warn("otel init", "err", err)
	} else {
		defer func() {
			sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer scancel()
			_ = otelShutdown(sctx)
		}()
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		slog.Error("db open failed", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		slog.Error("db migrate failed", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	authSvc := auth.NewService(database, cfg.JWTSecret)
	if username, password, created, err := authSvc.Bootstrap(ctx); err != nil {
		slog.Error("bootstrap failed", "err", err)
		os.Exit(1)
	} else if created {
		slog.Warn("bootstrap admin created — store this password, it will not be shown again",
			"username", username, "password", password)
	}

	// Docker is optional at startup: lack of daemon must not kill the server.
	var dockerCli *docker.Client
	if cli, err := docker.New(ctx); err != nil {
		slog.Warn("docker daemon unreachable — container endpoints will return 503", "err", err)
	} else {
		dockerCli = cli
		defer dockerCli.Close()
	}

	secretsSvc, err := secrets.New(cfg.SecretsKeyPath, cfg.SecretsEncryptEnv)
	if err != nil {
		slog.Error("secrets init failed", "err", err)
		os.Exit(1)
	}
	if secretsSvc.Enabled() {
		slog.Info("secrets encryption enabled", "recipient", secretsSvc.PublicRecipient())
	}

	stacksMgr, err := stacks.NewManager(cfg.StacksRoot, secretsSvc)
	if err != nil {
		slog.Error("stacks manager init failed", "err", err)
		os.Exit(1)
	}
	defer stacksMgr.Close()

	webFS, err := fs.Sub(webDist, "web_dist")
	if err != nil {
		slog.Warn("embedded web assets not available", "err", err)
	}

	composeSvc := compose.NewService(dockerCli, stacksMgr)
	auditSvc := audit.NewService(database, cfg.AuditGenesisPath)
	if err := auditSvc.EnsureGenesis(ctx); err != nil {
		slog.Error("audit genesis failed", "err", err)
		os.Exit(1)
	}

	// Vulnerability scanner — optional, logged as unavailable if the
	// grype binary is missing so the UI can show a helpful hint.
	var scannerSvc scanner.Scanner
	if cfg.ScannerEnabled {
		g := scanner.NewGrypeCLI(cfg.ScannerBinary)
		if err := g.Ready(); err != nil {
			slog.Warn("scanner disabled — install grype to enable CVE scans", "err", err)
		} else {
			scannerSvc = g
			slog.Info("scanner ready", "engine", "grype")
		}
	}
	scanStore := scanner.NewStore(database)

	// Proxy service is created with `enabled=false`; the real boot
	// decision is made below once the settings store is loaded so the
	// DB-backed `proxy_enabled` setting outranks the env var default.
	proxySvc := proxy.NewService(database, dockerCli, false)
	updaterSvc := updater.NewService(dockerCli, database)
	oidcSvc := oidc.NewService(database, authSvc, secretsSvc, cfg.BaseURL)

	metricsCol := metrics.NewCollector(database, dockerCli, 30*time.Second, metrics.DefaultRetention)
	metricsCol.Start(ctx)
	defer metricsCol.Stop()

	// Host-metrics sampler: smooths CPU% over a 5s rolling window so
	// the dashboard tiles don't jitter between polls. Runs for the
	// lifetime of the process; no-op on non-Linux builds.
	system.StartSampler(ctx)

	// P.11.9 — prometheus registry + background gauge refresher.
	// Wired via setter methods so the audit / alerts / middleware
	// packages don't import internal/metrics and create cycles.
	promMetrics := metrics.NewPromMetrics(database)
	promMetrics.StartRefresher(ctx)
	middleware.PromMetrics = promMetrics
	auditSvc.SetProm(promMetrics)

	notifySvc := notify.NewService(database)
	if err := notifySvc.Reload(ctx); err != nil {
		slog.Warn("notify reload", "err", err)
	}
	alertsSvc := alerts.NewService(database, notifySvc)
	alertsSvc.SetProm(promMetrics)
	alertsSvc.Start(ctx)
	defer alertsSvc.Stop()

	// The "system" backup source needs absolute paths to the server's
	// own DB, /stacks root, and data dir — the data dir is derived
	// from DBPath so operators who override DOCKMESH_DB_PATH get the
	// matching data dir automatically.
	backupPaths := backup.SystemPaths{
		DBPath:     cfg.DBPath,
		StacksRoot: cfg.StacksRoot,
		DataDir:    filepath.Dir(cfg.DBPath),
	}
	backupSvc := backup.NewService(database, dockerCli, stacksMgr, secretsSvc, backupPaths)
	if err := backupSvc.Start(ctx); err != nil {
		slog.Warn("backup scheduler start", "err", err)
	}
	// Default daily system backup (P.6.5). Idempotent — first boot
	// creates the job; subsequent boots find it already there. Users
	// who delete it are not pestered to recreate it.
	if err := backupSvc.EnsureDefaultJob(ctx); err != nil {
		slog.Warn("backup default job", "err", err)
	}
	defer backupSvc.Stop()

	// Auto-scaling controller (P.8). The ScaleFunc closure routes through
	// the local compose service for now — remote scaling will go through
	// the host abstraction once the agent binary is updated.
	scaleController := scaling.NewController(stacksMgr, metricsCol, func(ctx context.Context, stackName, service string, replicas int) error {
		if dockerCli == nil {
			return fmt.Errorf("docker unavailable")
		}
		detail, err := stacksMgr.Get(stackName)
		if err != nil {
			return err
		}
		lh := host.NewLocal(dockerCli)
		_, err = lh.ScaleService(ctx, stackName, detail.Compose, detail.Env, service, replicas)
		return err
	})
	scaleController.Start(ctx)
	defer scaleController.Stop()

	// Remote-agent PKI + service. The mTLS listener starts only if the
	// CA + server cert can be issued and a listen address is configured.
	pkiSANs := []string{}
	if cfg.AgentSANs != "" {
		for _, s := range strings.Split(cfg.AgentSANs, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				pkiSANs = append(pkiSANs, s)
			}
		}
	}
	// Resolve the PKI directory from DBPath so it follows
	// DOCKMESH_DB_PATH / data-dir overrides. The previous hardcoded
	// "./data" broke fresh installs under systemd because the
	// service's cwd is "/" — it tried to open /data/agents-ca.crt
	// and failed permission-denied.
	pkiMgr, err := pki.New(filepath.Dir(cfg.DBPath), pkiSANs)
	if err != nil {
		slog.Error("agent pki init failed", "err", err)
		os.Exit(1)
	}
	agentPublic := cfg.AgentPublicURL
	if agentPublic == "" {
		// Best-effort default: replace http(s) base URL host with wss + the
		// agent listener port. Operator should override via env in prod.
		agentPublic = deriveAgentURL(cfg.BaseURL, cfg.AgentListen)
	}
	agentsSvc := agents.NewService(database, pkiMgr, cfg.BaseURL, agentPublic)
	hostRegistry := host.NewRegistry(dockerCli, agentsSvc)

	// Wire host routing into the backup executor so jobs with host_id
	// != "" dispatch to the right agent. Must be post-construction
	// because backupSvc was created before hostRegistry existed.
	// FINDING-33 multi-host backup.
	backupSvc.SetHostResolver(backup.NewHostResolverFromRegistry(hostRegistry))

	// DB-backed system settings — reads from DB, falls back to env vars.
	settingsStore := settings.NewStore(database)
	if err := settingsStore.Load(ctx); err != nil {
		slog.Warn("settings store load", "err", err)
	}
	// P.12.1 — hand the settings store to auth.Service so password
	// policy + per-user lockout thresholds + rotation-day setting
	// are live-editable via the UI without a restart.
	authSvc.SetSettings(settingsStore)

	// Self-update checker: polls GitHub Releases once a day to surface
	// "Update available" banner in the UI. Admins can disable via the
	// update_check_enabled setting (air-gapped installs).
	selfUpdateChk := selfupdate.New(settingsStore, version.Version)
	selfUpdateChk.Start(ctx)

	// Proxy: the boot-time config flag is just the *default* — the
	// DB-backed `proxy_enabled` setting overrides it so an admin
	// flipping the toggle persists across restarts without env-var
	// edits. If enabled, bring the Caddy container up now (idempotent
	// — EnableProxy removes any stale container + reseeds bootstrap
	// config + pushes the current routes).
	if settingsStore.GetBool("proxy_enabled", cfg.ProxyEnabled) {
		if err := proxySvc.EnableProxy(ctx); err != nil {
			slog.Warn("proxy boot-up failed — toggle off/on in Settings once Docker is reachable", "err", err)
		}
	}

	globalEnvStore := globalenv.NewStore(database)

	// RBAC v2: DB-backed custom roles with in-memory cache.
	rolesStore := rbac.NewStore(database)
	if err := rolesStore.Load(ctx); err != nil {
		slog.Warn("rbac store load", "err", err)
	}
	middleware.RBACStore = rolesStore

	// API tokens for CI/CD (P.11.1). The service is stateless beyond
	// the DB; Start() just kicks the background last-used-at flusher.
	apiTokensSvc := apitokens.New(database)
	apiTokensSvc.Start(ctx)
	middleware.APITokensStore = apiTokensSvc

	// Registry credentials (P.11.7). Stateless — no background workers.
	// Passwords encrypt via the shared secrets service; without secrets
	// encryption the store still works but passwords sit as plaintext
	// bytes in the DB, same trade-off as the existing .env storage.
	registriesSvc := registries.New(database, secretsSvc)

	// Git-backed stacks (P.11.11). Cache dir sits under the DB data
	// dir so operators who override DOCKMESH_DB_PATH get the matching
	// location automatically. Auto-deploy closure uses LocalHost for
	// now; remote-agent git-auto-deploy is a follow-up slice.
	gitCacheDir := filepath.Join(filepath.Dir(cfg.DBPath), "git-cache")
	gitDeploy := func(ctx context.Context, stackName string) (any, error) {
		if dockerCli == nil {
			return nil, fmt.Errorf("docker unavailable")
		}
		detail, err := stacksMgr.Get(stackName)
		if err != nil {
			return nil, err
		}
		lh := host.NewLocal(dockerCli)
		return lh.DeployStack(ctx, stackName, detail.Compose, detail.Env)
	}
	gitSourceSvc := gitsource.New(database, secretsSvc, stacksMgr, gitCacheDir, gitDeploy)
	gitSourceSvc.Start(ctx)
	defer gitSourceSvc.Stop()

	// Stack templates (P.11.12). Seeds the built-in library from
	// embedded YAML on every boot so template fixes ship with the
	// binary. User-created templates are untouched — SeedBuiltins
	// only upserts rows where builtin=1.
	templatesSvc := templates.New(database)
	if err := templatesSvc.SeedBuiltins(ctx); err != nil {
		slog.Warn("stack templates seed", "err", err)
	}

	// Audit retention (P.11.13). The TargetWriter adapter turns a
	// backup_targets row into a live target.Build, streams NDJSON to
	// it on archive_target runs. Nil-safe if the backup-targets store
	// is ever unavailable — archive_target mode then reports an error.
	backupTargetStore := targets.NewTargetStore(database)
	auditTargetWriter := &auditTargetsAdapter{store: backupTargetStore}
	auditRetention := audit.NewRetention(database, auditSvc, settingsStore, auditTargetWriter)
	auditRetention.Start(ctx)
	defer auditRetention.Stop()

	// Audit webhook (P.11.14). Posts each audit entry to a configured
	// URL with optional HMAC signing + exponential-backoff retry.
	// Nil-safe — doesn't dispatch if URL is empty in settings.
	auditWebhook := audit.NewWebhook(settingsStore, settingsStore.Set)
	auditWebhook.Start(ctx)
	auditSvc.SetWebhook(auditWebhook)
	defer auditWebhook.Stop()

	// Agent upgrade controller (P.11.16) — auto / manual / staged
	// rollout modes. Starts a 60s evaluator loop that pushes
	// FrameReqAgentUpgrade to pending agents based on policy.
	agentUpgrade := agents.NewUpgradeController(agentsSvc, settingsStore)
	agentUpgrade.Start(ctx)

	// Host tags (P.11.2). In-memory cache loaded once at startup; kept
	// fresh after every mutation via Load() inside the service.
	hostTagsSvc := hosttags.New(database)
	if err := hostTagsSvc.Load(ctx); err != nil {
		slog.Warn("host tags load", "err", err)
	}

	deployStore := stacks.NewDeploymentStore(database)
	deployHistoryStore := stacks.NewHistoryStore(database)
	depStore := stacks.NewDependencyStore(database)
	migrationSvc := migration.NewService(database, hostRegistry, stacksMgr, deployStore)
	if err := migrationSvc.Start(ctx); err != nil {
		slog.Warn("migration service start", "err", err)
	}
	migrationSvc.StartCleaner(ctx)
	drainSvc := migration.NewDrainService(migrationSvc, database)

	loginLimiter := ratelimit.New(10, time.Minute, 5*time.Minute)
	h := handlers.New(handlers.Deps{
		DB:           database,
		Auth:         authSvc,
		Audit:        auditSvc,
		Docker:       dockerCli,
		Stacks:       stacksMgr,
		Deployments:  deployStore,
		DeployHistory: deployHistoryStore,
		Dependencies: depStore,
		Compose:      composeSvc,
		LoginLimiter: loginLimiter,
		Scanner:      scannerSvc,
		ScanStore:    scanStore,
		Proxy:        proxySvc,
		Updater:      updaterSvc,
		OIDC:         oidcSvc,
		Metrics:      metricsCol,
		Notify:       notifySvc,
		Alerts:       alertsSvc,
		Backups:       backupSvc,
		BackupTargets: backupTargetStore,
		Secrets:       secretsSvc,
		Migrations:   migrationSvc,
		Drains:       drainSvc,
		Agents:       agentsSvc,
		Hosts:        hostRegistry,
		HostTags:     hostTagsSvc,
		Roles:        rolesStore,
		Settings:     settingsStore,
		GlobalEnv:    globalEnvStore,
		APITokens:    apiTokensSvc,
		Registries:   registriesSvc,
		GitSource:    gitSourceSvc,
		Templates:      templatesSvc,
		AuditRetention: auditRetention,
		AuditWebhook:   auditWebhook,
		AgentUpgrade:   agentUpgrade,
		Prom:           promMetrics,
		SelfUpdate:     selfUpdateChk,
		JWTSecret:    cfg.JWTSecret,
	})
	router := api.NewRouter(h, authSvc, webFS, cfg.MetricsAuth)

	// Backfill stack deployments (P.7): scan local containers to detect
	// which stacks are already deployed. Remote-agent containers are
	// handled lazily — agents reconnect after boot and their containers
	// will be picked up on the next deploy or via a future sync.
	if dockerCli != nil {
		go func() {
			bgCtx := context.Background()
			cli := dockerCli.Raw()
			all, err := cli.ContainerList(bgCtx, container.ListOptions{All: true})
			if err != nil {
				slog.Warn("backfill: container list", "err", err)
				return
			}
			infos := make([]stacks.ContainerInfo, len(all))
			for i, c := range all {
				infos[i] = stacks.ContainerInfo{Labels: c.Labels, HostID: "local"}
			}
			if err := stacks.BackfillDeployments(bgCtx, deployStore, stacksMgr, infos); err != nil {
				slog.Warn("backfill: deployments", "err", err)
			}
		}()
	}

	// mTLS listener for agents (concept §3.1). Started in its own
	// goroutine; failures are logged but don't take down the main API.
	if cfg.AgentListen != "" {
		tlsCfg, err := agents.ServerTLSConfig(pkiMgr)
		if err != nil {
			slog.Error("agent tls config", "err", err)
		} else {
			agentMux := http.NewServeMux()
			agentMux.Handle("/connect", agents.NewWSHandler(agentsSvc, pkiMgr))
			agentSrv := &http.Server{
				Addr:              cfg.AgentListen,
				Handler:           agentMux,
				TLSConfig:         tlsCfg,
				ReadHeaderTimeout: 10 * time.Second,
			}
			go func() {
				slog.Info("agent mtls listening", "addr", cfg.AgentListen, "public_url", agentPublic)
				if err := agentSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
					slog.Error("agent listener error", "err", err)
				}
			}()
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = agentSrv.Shutdown(shutdownCtx)
			}()
		}
	}

	// P.12.3 — wrap the router in otelhttp so every request gets a
	// server-side span (route template, method, status, duration).
	// When OTel is disabled (Endpoint empty) the global tracer is a
	// no-op so this adds negligible overhead.
	tracedRouter := otelhttp.NewHandler(router, "dockmesh.http",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}))
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           tracedRouter,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	// P.12.2 — graceful shutdown.
	//
	// Flip the readiness flag FIRST so any load balancer in front
	// of us starts routing new traffic elsewhere (every LB I've seen
	// polls readiness every few seconds; give it a moment to catch
	// up before we start rejecting). Then Shutdown() stops accepting
	// new connections and waits up to 30s for in-flight handlers
	// (including WebSocket log / exec streams) to finish cleanly.
	slog.Info("shutting down — draining readiness")
	handlers.MarkShuttingDown()
	time.Sleep(2 * time.Second)
	slog.Info("shutdown: stop accepting new connections")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "err", err)
	}
}

// auditTargetsAdapter bridges audit.Retention's TargetWriter interface
// to the backup_targets store. Loads the target row, builds the live
// target via targets.Build, opens a writer for the given name, and
// streams the archive bytes through.
type auditTargetsAdapter struct {
	store *targets.TargetStore
}

func (a *auditTargetsAdapter) WriteFile(ctx context.Context, targetID int64, name string, body io.Reader) error {
	stored, err := a.store.Get(ctx, targetID)
	if err != nil {
		return err
	}
	t, err := targets.Build(stored.Type, stored.Config)
	if err != nil {
		return err
	}
	w, err := t.Open(ctx, name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, body); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// deriveAgentURL builds a default wss:// URL for the agent listener from
// the API base URL and the agent listen address. Operator can override
// with DOCKMESH_AGENT_PUBLIC_URL — recommended in production.
// buildLogger returns a slog.Logger honouring the configured format
// + level. Called AFTER config.Load so the bootstrap JSON logger
// handles the narrow window between process start and config parse.
func buildLogger(format, level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}

func deriveAgentURL(baseURL, listen string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return "wss://localhost" + listen + "/connect"
	}
	host := u.Hostname()
	port := strings.TrimPrefix(listen, ":")
	return "wss://" + host + ":" + port + "/connect"
}
