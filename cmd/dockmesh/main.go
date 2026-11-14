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
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh"
	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/api"
	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/apitokens"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/deploy"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/gitsource"
	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/dockmesh/dockmesh/internal/invites"
	"github.com/dockmesh/dockmesh/internal/layout"
	"github.com/dockmesh/dockmesh/internal/ldapauth"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/dockmesh/dockmesh/internal/oauth2auth"
	"github.com/dockmesh/dockmesh/internal/oidc"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/pki"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/registries"
	"github.com/dockmesh/dockmesh/internal/restore"
	"github.com/dockmesh/dockmesh/internal/saml"
	"github.com/dockmesh/dockmesh/internal/scaling"
	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
	"github.com/dockmesh/dockmesh/internal/settings"
	"github.com/dockmesh/dockmesh/internal/setup"
	"github.com/dockmesh/dockmesh/internal/stackhelper"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/system"
	"github.com/dockmesh/dockmesh/internal/telemetry"
	"github.com/dockmesh/dockmesh/internal/templates"
	"github.com/dockmesh/dockmesh/internal/updategate"
	"github.com/dockmesh/dockmesh/internal/updater"
	"github.com/dockmesh/dockmesh/internal/userprefs"
	"github.com/dockmesh/dockmesh/pkg/version"
	"github.com/moby/moby/client"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

//go:embed all:web_dist
var webDist embed.FS

// loadedEnvFile is the env file loadEnvFileFromArgs read, empty when the
// environment came from elsewhere (systemd's EnvironmentFile=, a shell).
var loadedEnvFile string

// explicitEnvFile is true when loadedEnvFile came from --env-file.
var explicitEnvFile bool

// serverEnvFile names the env file this server most likely runs with: the
// one loaded from --env-file or the default location, then the config in
// /etc/dockmesh, else the file older installs keep next to the data dir.
// Empty when none is known.
func serverEnvFile(dbPath string) string {
	if loadedEnvFile != "" {
		return loadedEnvFile
	}
	// The service user may not be in the config's group, so a file it
	// cannot stat counts as present.
	if _, err := os.Lstat(layout.ConfigFile); err == nil || errors.Is(err, fs.ErrPermission) {
		return layout.ConfigFile
	}
	if dbPath == "" {
		return ""
	}
	guess := filepath.Clean(filepath.Join(filepath.Dir(dbPath), "..", "dockmesh.env"))
	if _, err := os.Stat(guess); err == nil {
		return guess
	}
	return ""
}

// seedScannerEnabled saves the env default when no choice is stored yet, so
// the settings page shows the state scans actually follow.
func seedScannerEnabled(ctx context.Context, store *settings.Store, enabled bool) error {
	switch store.Stored(settings.KeyScannerEnabled) {
	case "true", "false":
		return nil
	}
	return store.Set(ctx, settings.KeyScannerEnabled, strconv.FormatBool(enabled))
}

// loadEnvFileFromArgs scans os.Args for --env-file=PATH or
// --env-file PATH, loads that file as KEY=VALUE env vars (skipping
// `#`-prefixed comments + blank lines), and strips the flag out of
// os.Args so the rest of the process never sees it.
//
// If no --env-file was passed, we also try the default location where
// install.sh and `dockmesh init` write the env file (see defaultEnvFiles).
// This lets CLI commands like `sudo dockmesh admin list-users` find the DB + PKI
// without the operator having to source the file or pass --env-file
// every time.
//
// Originally built because launchd on macOS has no EnvironmentFile
// directive (unlike systemd), so init's plist template passes
// `--env-file PATH` to `dockmesh serve` explicitly.
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
	explicit := path != ""
	if !explicit {
		// Auto-detect a well-known env file location. Only load if the
		// operator hasn't already set the data paths in their shell —
		// an explicit env var always wins over the auto-loaded file.
		if _, already := os.LookupEnv("DOCKMESH_DB_PATH"); already {
			return nil
		}
		if _, already := os.LookupEnv("DOCKMESH_DATA_DIR"); already {
			return nil
		}
		path = firstExistingFile(defaultEnvFiles)
		if path == "" {
			return nil
		}
	} else {
		os.Args = keep
		explicitEnvFile = true
	}
	var data []byte
	var err error
	if explicit {
		data, err = os.ReadFile(path) // #nosec G703 -- explicit --env-file chosen by the operator running the CLI
	} else {
		data, err = readTrustedEnvFile(path)
		if errors.Is(err, errUntrustedEnvFile) {
			fmt.Fprintf(os.Stderr, "dockmesh: not loading %s (%v); pass --env-file to use it\n", path, err)
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	loadedEnvFile = path
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

// countServicesString returns "5 services" / "1 service" for use in
// notification bodies. Tiny but used in a couple of producer paths.
func countServicesString(n int) string {
	if n == 1 {
		return "1 service"
	}
	return fmt.Sprintf("%d services", n)
}

// restartRequested makes main exit with exitRestart after a normal
// shutdown, so the service manager starts it again (systemd
// Restart=on-failure, Docker restart policies). Used to apply a staged
// system restore.
var restartRequested atomic.Bool

const exitRestart = 75

func main() {
	// Registered first so it runs after every other deferred cleanup.
	defer func() {
		if restartRequested.Load() {
			slog.Info("exiting for restart")
			os.Exit(exitRestart)
		}
	}()
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
		case "system":
			runSystemCmd(os.Args[2:])
			return
		case "update":
			runUpdateCmd(os.Args[2:])
			return
		case "setup":
			runSetupCmd(os.Args[2:])
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

	// Before config.Load and db.Open: they create the secrets file and the
	// database a fresh install lacks.
	setupFacts := setup.Probe{
		ConfigFiles: []string{loadedEnvFile, unitEnvFile()},
		DBPathSet:   os.Getenv("DOCKMESH_DB_PATH") != "",
		Force:       os.Getenv("DOCKMESH_SETUP_FORCE") == "true",
		Paths:       config.ResolvePaths(os.Getenv),
	}.Scan()

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

	// A staged system restore is swapped in before anything opens the
	// database. If the swap stops halfway it resumes on the next start;
	// serving from a half-swapped install is never an option.
	if applied, err := restore.ApplyStaged(cfg); err != nil {
		slog.Error("applying the staged system restore failed; fix the error and start again", "err", err)
		os.Exit(1)
	} else if applied {
		slog.Warn("system backup restored; the replaced files are kept next to the data and stacks directories", "dir", restore.PreRestoreDir)
		// The config read secrets.env (the token signing secret) from the
		// replaced install; the restored one has to be used from now on.
		if cfg, err = config.Load(); err != nil {
			slog.Error("config load after the restore failed", "err", err)
			os.Exit(1)
		}
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
	if err := db.CompactIfPending(context.Background(), database); err != nil {
		slog.Warn("db compaction failed, retrying on the next start", "err", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	authSvc := auth.NewService(database, cfg.JWTSecret)

	setupState, setupMode, setupForceIgnored, err := setup.NewForBoot(ctx, database, setupFacts, "")
	if err != nil {
		slog.Error("setup state init failed", "err", err)
		os.Exit(1)
	}
	setupEnvFile := serverEnvFile(cfg.DBPath)
	if setupForceIgnored {
		slog.Warn("DOCKMESH_SETUP_FORCE ignored: this installation is set up, so the setup wizard stays closed — remove the flag from the env file",
			"env_file", setupEnvFile)
	}
	if setupState.Active() {
		slog.Warn("Setup mode. Get a setup code with: "+setup.CodeCommand, "install", setupMode.String())
	} else if exists, err := setup.UsersExist(ctx, database); err != nil {
		slog.Warn("check for user accounts", "err", err)
	} else if !exists {
		slog.Error("this installation has no user accounts and is not a new install, so the setup wizard stays closed; create an admin on this server, then restart the service",
			"command", setup.RecoveryCommand, "env_file", setupFacts.EnvFile,
			"database", setupFacts.DB, "other_database", setupFacts.ForeignDB, "data", setupFacts.Data)
	} else if admin, err := setup.AdminExists(ctx, database); err != nil {
		slog.Warn("check for an admin account", "err", err)
	} else if !admin {
		slog.Error("no account holds an admin role, so nobody can manage users or settings; the setup wizard stays closed — create an admin on this server, then restart the service",
			"command", setup.RecoveryCommand)
	}

	// Docker may not be up yet when we boot (macOS launchd fires us
	// before Docker Desktop has opened its socket; Linux systemd can
	// hit the same race with Docker.service still starting). The client
	// wrapper tolerates that: construction doesn't block on a ping, and
	// a background monitor flips the `connected` flag back on once the
	// socket appears. No dockmesh restart needed when Docker comes up
	// later — container endpoints just start returning 200 again.
	dockerCli, err := docker.New(ctx)
	if err != nil {
		slog.Error("docker client init failed (not a daemon problem — client config is broken)", "err", err)
		os.Exit(1)
	}
	defer dockerCli.Close()
	if !dockerCli.Connected() {
		slog.Warn("docker daemon not reachable yet — container endpoints will return 503 until it comes up",
			"err", dockerCli.LastError())
	}
	// The server's own container is protected like the proxy: non-admins
	// can't stop it or exec into it, and nobody reads its files.
	go func() {
		for !dockerCli.Connected() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		if id := host.DetectSelfContainer(ctx, dockerCli.Raw()); id != "" {
			host.SetSelfContainerID(id)
			slog.Info("running in a container", "container", id[:12])
		}
	}()

	// Let the system-metrics sampler query Docker's configured limits
	// (Docker Desktop VM caps on macOS, cgroup limits on constrained
	// Linux hosts). When they diverge from raw host hardware, the
	// dashboard shows both so operators can see their Docker allocation
	// against their actual machine.
	//
	// On top of the `info` call we also aggregate running-container
	// stats when we detect a Docker-limited setup — the host's CPU%
	// and memory usage numbers measure the whole Mac (macOS + every
	// app), which has no meaningful relationship to what's actually
	// happening inside the Docker VM. Per-container stats are the
	// authoritative "how utilised is my Docker allocation" answer.
	system.SetDockerInfoFn(func(ctx context.Context) (system.DockerSnapshot, bool) {
		if !dockerCli.Connected() {
			return system.DockerSnapshot{}, false
		}
		info, err := dockerCli.Info(ctx)
		if err != nil {
			return system.DockerSnapshot{}, false
		}
		system.SetDockerRoot(info.DockerRootDir)
		snap := system.DockerSnapshot{
			NCPU:     info.NCPU,
			MemTotal: uint64(info.MemTotal),
		}
		// Only pay the container-stats-aggregation cost when Docker's
		// view diverges from the host — on a plain Linux server the
		// numbers match and we'd be doing work the sampler immediately
		// discards.
		hostCores := runtime.NumCPU()
		if info.NCPU != hostCores || needsDockerLimitCheck() {
			cpuPct, memUsed := aggregateDockerStats(ctx, dockerCli)
			snap.CPUPercent = cpuPct
			snap.MemUsed = memUsed
		}
		return snap, true
	})

	secretsSvc, err := secrets.New(cfg.SecretsKeyPath, cfg.SecretsEncryptEnv)
	if err != nil {
		slog.Error("secrets init failed", "err", err)
		os.Exit(1)
	}
	if secretsSvc.Enabled() {
		slog.Info("secrets encryption enabled", "recipient", secretsSvc.PublicRecipient())
	}

	system.SetRolePathsFn(serverRolePaths(database, dockerCli, cfg.StacksRoot, filepath.Dir(cfg.DBPath)))

	stacksMgr, err := stacks.NewManager(cfg.StacksRoot, secretsSvc)
	if err != nil {
		slog.Error("stacks manager init failed", "err", err)
		os.Exit(1)
	}
	defer stacksMgr.Close()
	stacksMgr.SetSumStore(stacks.NewSumStore(database))
	stacksMgr.SetFileHelper(stackhelper.New(dockerCli.Raw()))
	stacksMgr.RecordUntrackedSums()

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

	// Built whether or not scanning is on: the setting is read per scan and
	// the binary can appear later, so neither needs a restart.
	scannerCLI := scanner.NewGrypeCLI(cfg.ScannerBinary)
	if archiveDir, dbDir, err := scanner.PrepareDirs(filepath.Dir(cfg.DBPath)); err != nil {
		slog.Warn("scanner directories unavailable, using grype's defaults", "err", err)
	} else {
		scannerCLI.ArchiveDir, scannerCLI.DBDir = archiveDir, dbDir
	}
	if err := scannerCLI.Ready(); err != nil {
		slog.Warn("grype not found — install it to enable CVE scans", "err", err)
	} else {
		slog.Info("scanner ready", "engine", "grype")
	}
	scanStore := scanner.NewStore(database)

	// Proxy service is created with `enabled=false`; the real boot
	// decision is made below once the settings store is loaded so the
	// DB-backed `proxy_enabled` setting outranks the env var default.
	// Operations left running by the previous process are failed before
	// anything can start a new one.
	opsSvc := operations.New(database)
	if n, err := opsSvc.MarkInterrupted(ctx); err != nil {
		slog.Warn("operations: mark interrupted", "err", err)
	} else if n > 0 {
		slog.Info("operations: marked interrupted after restart", "count", n)
	}
	opsSvc.StartPruner(ctx)

	proxySvc := proxy.NewService(database, dockerCli, false)
	proxySvc.SetImage(cfg.ProxyImage)
	proxySvc.SetAdminSocketDir(filepath.Dir(cfg.DBPath))
	proxySvc.SetInContainer(proxy.DetectContainer(host.SelfContainerID))
	stackLocks := stacklock.New()
	updaterSvc := updater.NewService(dockerCli, database)
	updaterSvc.SetLocks(stackLocks)
	updaterSvc.SetRollbackKeep(cfg.UpdateRollbackKeep)
	// Background image-update watcher: every 30 minutes asks each
	// running container's registry for a fresher manifest. Result is
	// surfaced via /api/v1/images/updates and as image_update_available
	// on the container list. Local host only. Started once the notifier
	// and registry credentials are wired.
	updateWatcher := updater.NewWatcher(dockerCli, database, 30*time.Minute)
	defer updateWatcher.Stop()
	oidcSvc := oidc.NewService(database, authSvc, secretsSvc, cfg.BaseURL)
	samlSvc := saml.NewService(database, authSvc, secretsSvc, cfg.BaseURL)
	ldapSvc := ldapauth.NewService(database, authSvc, secretsSvc)
	oauth2Svc := oauth2auth.NewService(database, authSvc, secretsSvc, cfg.BaseURL)

	jobGate := updategate.New(config.ResolvePaths(os.Getenv).UpdateDir)

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
	notifySvc.SetEmailLanguages(userprefs.New(database).LanguageForEmail)
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
	backupSvc.SetOperations(opsSvc)
	// Before the gate: a manual run started during an update window must
	// not be taken for one the previous process left behind.
	backupSvc.FailInterruptedRuns(ctx)
	jobGate.Run(ctx, "backup scheduler", func() {
		if err := backupSvc.Start(ctx); err != nil {
			slog.Warn("backup scheduler start", "err", err)
		}
	})
	// P.13.2: backup defaults are now opt-in. New installs never get a
	// default job created on boot; the user has to set one up
	// explicitly (Backups → New job, or the "Enable daily system
	// backup" toggle). Existing installs that still carry the legacy
	// auto-created `dockmesh-system` job get the needs_review flag set
	// once so the operator can confirm Keep / Disable on the next UI
	// visit. Idempotent — once cleared, the flag stays cleared.
	if err := backupSvc.MarkAutoCreatedJobsForReview(ctx); err != nil {
		slog.Warn("backup default review migration", "err", err)
	}
	// Before seedLocalBackupRoots, so a kept legacy directory becomes an
	// allowed root on the first start.
	sysPathFrom, sysPathTo, err := backupSvc.MigrateSystemJobPath(ctx)
	if err != nil {
		slog.Warn("system backup path migration", "err", err)
	}
	defer backupSvc.Stop()

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
	// DB-backed system settings — reads from DB, falls back to env vars.
	settingsStore := settings.NewStore(database)
	if err := settingsStore.Load(ctx); err != nil {
		slog.Warn("settings store load", "err", err)
	}
	if err := seedScannerEnabled(ctx, settingsStore, cfg.ScannerEnabled); err != nil {
		slog.Warn("scanner setting not saved", "err", err)
	}
	// Sign-in callbacks and invite links use the same base URL as the
	// agent installer: DOCKMESH_BASE_URL, else the Base URL setting.
	publicBaseURL := func() string { return agentEndpoints(cfg, settingsStore).BaseURL }
	oidcSvc.SetBaseURL(publicBaseURL)
	samlSvc.SetBaseURL(publicBaseURL)
	samlSvc.StartMetadataRefresh(ctx)
	oauth2Svc.SetBaseURL(publicBaseURL)
	agentsSvc := agents.NewService(database, pkiMgr, cfg.BaseURL, "")
	agentsSvc.SetEndpointResolver(func() agents.Endpoints {
		return agentEndpoints(cfg, settingsStore)
	})
	agentsSvc.SetListenerEnabled(cfg.AgentListen != "")
	agentEP, err := agentsSvc.ReloadEndpoints()
	if err != nil {
		slog.Warn("agent server certificate does not cover the agent URL host; agents dialing it will fail TLS verification",
			"agent_url", agentEP.AgentURL, "err", err)
	}
	agentsSvc.SetMetricsSink(metricsCol.StoreAgentBatch)
	agentsSvc.SetEnrollTokenTTL(cfg.AgentEnrollTokenTTL)
	hostRegistry := host.NewRegistry(dockerCli, agentsSvc)

	// Wire host routing into the backup executor so jobs with host_id
	// != "" dispatch to the right agent. Must be post-construction
	// because backupSvc was created before hostRegistry existed.
	// FINDING-33 multi-host backup.
	backupSvc.SetHostResolver(backup.NewHostResolverFromRegistry(hostRegistry))

	if err := seedLocalBackupRoots(ctx, settingsStore, database, backupSvc, filepath.Dir(cfg.DBPath)); err != nil {
		slog.Warn("seed local backup roots", "err", err)
	}
	targets.SetLocalRoots(func() []string {
		return targets.ParseRoots(settingsStore.Get(targets.LocalRootsSetting, ""))
	})
	if wd, err := os.Getwd(); err != nil {
		slog.Warn("working directory unknown; local backup targets without a path are refused", "err", err)
	} else if dir, err := pinEmptyLocalPath(ctx, settingsStore, filepath.Dir(cfg.DBPath), wd); err != nil {
		slog.Warn("pin the directory for local backup targets without a path", "err", err)
	} else {
		targets.SetEmptyPathDir(dir)
	}
	if tz := settingsStore.Get(backup.TimezoneSetting, ""); tz != "" {
		if err := backupSvc.SetTimezone(ctx, tz); err != nil {
			slog.Warn("backup time zone setting ignored, using the server time zone", "err", err)
		}
	}
	// P.12.1 — hand the settings store to auth.Service so password
	// policy + per-user lockout thresholds + rotation-day setting
	// are live-editable via the UI without a restart.
	authSvc.SetSettings(settingsStore)

	// Session hygiene: idle-revoke + hard-delete of dead rows. Runs
	// hourly. Settings (idle TTL, retention) come from the SignInConfig
	// store so admins can tune them via the Authentication page.
	sessionCleanup := auth.NewSessionCleanup(database, settingsStore)
	sessionCleanup.Start(ctx)
	defer sessionCleanup.Stop()

	// Self-update checker: polls GitHub Releases every 2 hours by default to surface
	// "Update available" banner in the UI. Admins can disable via the
	// update_check_enabled setting (air-gapped installs).
	selfUpdateChk := selfupdate.New(settingsStore, version.Version)
	selfUpdateChk.Start(ctx)

	globalEnvStore := globalenv.NewStore(database, secretsSvc)
	if n, err := globalEnvStore.EncryptFlagged(ctx); err != nil {
		slog.Warn("global env: encrypt stored secret values", "err", err)
	} else if n > 0 {
		slog.Info("global env: encrypted stored secret values", "count", n)
	}

	// Roles and assignments, cached in memory; built-in role definitions
	// follow the binary.
	rolesStore := rbac.NewStore(database)
	if err := rolesStore.SeedBuiltins(ctx); err != nil {
		slog.Error("seed built-in roles", "err", err)
		os.Exit(1)
	}
	if err := rolesStore.Load(ctx); err != nil {
		slog.Error("load roles and assignments", "err", err)
		os.Exit(1)
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

	// Notification center backing store. Producers (gitDeploy, alerts,
	// backups, …) call Emit so the bell-icon dropdown can render a
	// durable timeline of platform events. Emit with empty UserID fans
	// out across every user — the directory wiring below feeds that.
	notifSvc := notifications.NewService(database)
	notifSvc.SetUserDirectory(authSvc)

	// Action-required notices. Packages register startup checks on
	// noticesSvc; they run once every service below is wired.
	noticesSvc := notices.NewService(database)
	noticesSvc.Register("backups.local-roots-review", func(ctx context.Context, n *notices.Service) error {
		return syncSeededRootsNotice(ctx, n, settingsStore)
	})
	noticesSvc.Register(saml.RekeyNoticeKey, samlSvc.SyncRekeyNotice)
	noticesSvc.RegisterRefresh(saml.RekeyNoticeKey, samlSvc.SyncRekeyNotice)
	noticesSvc.Register(auth.SSORekeyNoticeKey, authSvc.SyncSSORekeyNotice)
	noticesSvc.RegisterRefresh(auth.SSORekeyNoticeKey, authSvc.SyncSSORekeyNotice)
	noticesSvc.Register("backups.system-job-path", func(ctx context.Context, n *notices.Service) error {
		return raiseSystemPathNotice(ctx, n, sysPathFrom, sysPathTo)
	})
	noticesSvc.Register("backups.system-target-moved", func(ctx context.Context, n *notices.Service) error {
		return syncMovedSystemTargetNotice(ctx, n, backupSvc)
	})
	noticesSvc.Register(restore.LeftoverNoticeKey, func(ctx context.Context, n *notices.Service) error {
		return restore.SyncLeftoverNotice(ctx, cfg, n)
	})
	noticesSvc.Register("backups.encrypt-unavailable", func(ctx context.Context, n *notices.Service) error {
		return syncUnencryptableNotice(ctx, n, backupSvc)
	})
	noticesSvc.Register("stacks.staged-bind-mounts", func(ctx context.Context, n *notices.Service) error {
		return syncStagedMountNotices(ctx, n, hostRegistry)
	})
	stacksMgr.SetNotices(noticesSvc)
	hostRegistry.SetAfterDeploy(func(ctx context.Context, hostID, stack string, res *compose.DeployResult) {
		stacksMgr.Deployed(ctx, stack)
		reportBindCopies(ctx, noticesSvc, hostID, stack, res)
		if err := syncStagedMountNotices(ctx, noticesSvc, hostRegistry); err != nil {
			slog.Warn("staged bind mount notices", "err", err)
		}
	})
	noticesSvc.Register("api-tokens.legacy", apiTokensSvc.SyncLegacyNotice)
	proxySvc.SetNotices(noticesSvc)
	proxySvc.SetSettings(settingsStore)

	// Proxy: the boot-time config flag is just the *default* — the
	// DB-backed `proxy_enabled` setting overrides it so an admin
	// flipping the toggle persists across restarts without env-var
	// edits. If enabled, reconcile the Caddy container (kept when its
	// spec is current) and push the current routes.
	if _, err := proxySvc.Reconcile(ctx, settingsStore, cfg.ProxyEnabled); err != nil {
		slog.Warn("proxy boot-up failed — toggle off/on in Settings once Docker is reachable", "err", err)
	}
	// Start the ACME event tailer regardless of current proxy state —
	// the loop is dormant when the proxy is disabled and reconnects
	// automatically once the container appears.
	proxySvc.StartACMETailer(ctx)
	proxySvc.StartWatchdog(ctx, 30*time.Second, func() bool {
		return settingsStore.GetBool("proxy_enabled", cfg.ProxyEnabled)
	})
	noticesSvc.Register("backups.job-name-collision", backupSvc.SyncNameCollisionNotice)
	noticesSvc.Register(setup.ForceIgnoredNoticeKey, setup.ForceNoticeCheck(setupForceIgnored, setupEnvFile))
	noticesSvc.Register(layout.ConfigNoticeKey, layout.ConfigNoticeCheck(setupEnvFile))
	noticesSvc.Register(setup.NoAdminNoticeKey, setup.NoAdminNoticeCheck(database))

	// Wire the registries service into every code path that pulls an
	// image. Without this, compose deploys + the "update image" button
	// + git-source auto-deploys all hit the daemon API anonymously and
	// private registries (GHCR, ECR, Quay, …) reply 401.
	// In-memory cache; the service reloads it after every mutation.
	hostTagsSvc := hosttags.New(database)
	if err := hostTagsSvc.Load(ctx); err != nil {
		slog.Warn("host tags load", "err", err)
	}
	registriesSvc.SetHostTags(hostTagsSvc.Tags)
	localAuth := registriesSvc.ForHost("local")
	composeSvc.SetAuthResolver(localAuth)
	updaterSvc.SetAuthResolver(localAuth)
	updateWatcher.SetAuthResolver(localAuth)
	hostRegistry.Local().SetAuthResolver(localAuth)
	hostRegistry.SetRegistryAuth(registriesSvc)
	hostRegistry.Local().SetStackDirResolver(stacksMgr.Dir)

	// Deploy progress tracker — same propagation pattern as the auth
	// resolver. Frontend polls /stacks/{name}/deploy/progress for the
	// live phase + service + elapsed indicator.
	deployTracker := deploy.NewTracker()
	composeSvc.SetProgressTracker(deployTracker)
	hostRegistry.Local().SetProgressTracker(deployTracker)

	// Created before the git deployer, which records an entry per
	// git-triggered deploy.
	deployHistoryStore := stacks.NewHistoryStore(database)
	updaterSvc.SetStacks(stacksMgr, deployHistoryStore)

	deployStore := stacks.NewDeploymentStore(database)

	// The auto-scaler acts on the host a stack is deployed to.
	scaleController := scaling.NewController(stacksMgr, metricsCol,
		func(ctx context.Context, stack, service string) (scaling.Target, error) {
			hostID, err := deployStore.HostOf(ctx, stack)
			if err != nil {
				return nil, err
			}
			h, err := hostRegistry.Pick(hostID)
			if err != nil {
				return nil, err
			}
			return scaling.NewHostTarget(h, stack, service), nil
		},
		scaling.CheckedScale(stacksMgr, stackLocks, deployStore))
	jobGate.Run(ctx, "auto-scaler", func() { scaleController.Start(ctx) })
	defer scaleController.Stop()

	// Git-backed stacks. Cache dir sits under the DB data dir so
	// operators who override DOCKMESH_DB_PATH get the matching location
	// automatically.
	gitCacheDir := filepath.Join(filepath.Dir(cfg.DBPath), "git-cache")
	// gitSourceSvc is set below; the deployer reads the synced commit
	// from it.
	var gitSourceSvc *gitsource.Service
	gitDeploy := &gitDeployer{
		stacks:      stacksMgr,
		deployments: deployStore,
		history:     deployHistoryStore,
		hosts:       hostRegistry,
		globals:     globalEnvStore.List,
		notifs:      notifSvc,
		locks:       stackLocks,
		commit: func(ctx context.Context, stack string) string {
			if src, err := gitSourceSvc.Get(ctx, stack); err == nil && src != nil {
				return src.LastSyncSHA
			}
			return ""
		},
		clearPending: func(ctx context.Context, stack string) error { return gitSourceSvc.ClearPending(ctx, stack) },
	}
	gitSourceSvc = gitsource.New(database, secretsSvc, stacksMgr, gitCacheDir, gitDeploy.deploy)
	gitSourceSvc.SetDeployNow(gitDeploy.deployNow)
	stackLocks.OnRelease(func(k stacklock.Key) {
		// Pending git deploys are background work too; the gated start
		// of gitSourceSvc deploys them once the update is confirmed.
		if jobGate.Pending() {
			return
		}
		gitSourceSvc.StackReleased(k.Stack, gitTargetMatcher(gitDeploy, k.Target))
	})
	noticesSvc.Register("git-webhook.no-secret", gitSourceSvc.SyncWebhookNotice)
	jobGate.Run(ctx, "git polling", func() { gitSourceSvc.Start(ctx) })
	defer gitSourceSvc.Stop()

	// Stack templates (P.11.12). Seeds the built-in library from
	// embedded YAML on every boot so template fixes ship with the
	// binary. User-created templates are untouched — SeedBuiltins
	// only upserts rows where builtin=1.
	templatesSvc := templates.New(database)
	// Invite-link service for the Coolify-style "Invite user" flow.
	// Generates one-time tokens an admin shares via Slack/email/etc.
	// Cleanup goroutine deletes used + expired rows after 30 days.
	invitesSvc := invites.New(database, cfg.BaseURL)
	invitesSvc.SetBaseURL(publicBaseURL)
	invitesSvc.Start(ctx)
	defer invitesSvc.Stop()
	if err := templatesSvc.SeedBuiltins(ctx); err != nil {
		slog.Warn("stack templates seed", "err", err)
	}

	// Audit retention. The TargetWriter adapter turns a backup_targets
	// row into a live target.Build, streams NDJSON to it on
	// archive_target runs.
	backupTargetStore := backupSvc.Targets()
	auditTargetWriter := &auditTargetsAdapter{store: backupTargetStore, notices: noticesSvc}
	auditRetention := audit.NewRetention(database, auditSvc, settingsStore, auditTargetWriter, cfg.DataDir)
	jobGate.Run(ctx, "audit retention", func() { auditRetention.Start(ctx) })
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
	jobGate.Run(ctx, "agent upgrades", func() { agentUpgrade.Start(ctx) })
	agentsSvc.SetNotices(noticesSvc)
	go agentsSvc.RunCertNotices(ctx, noticesSvc)

	updateInstaller, err := newUpdateInstaller(ctx, stackLocks, agentsSvc, noticesSvc, auditSvc)
	if err != nil {
		slog.Warn("update installer unavailable", "err", err)
	}

	notifSvc.SetAccessChecker(&notifications.RBACAccess{Access: rolesStore, Tags: hostTagsSvc})

	depStore := stacks.NewDependencyStore(database)
	migrationSvc := migration.NewService(database, hostRegistry, stacksMgr, deployStore)
	migrationSvc.SetGlobalEnv(globalEnvStore.List)
	migrationSvc.SetUsage(metricsCol)
	if err := migrationSvc.Start(ctx); err != nil {
		slog.Warn("migration service start", "err", err)
	}
	jobGate.Run(ctx, "migration cleaner", func() { migrationSvc.StartCleaner(ctx) })
	// Deferred after every gated start, so it runs before their Stops.
	defer jobGate.Close()
	drainSvc := migration.NewDrainService(migrationSvc, database)

	// Bell-icon producer wire-up. All services that emit notifications
	// get the shared notifSvc here, after every dependency has been
	// constructed. Producers tolerate nil — if we forgot to wire one,
	// it just stays silent instead of crashing.
	alertsSvc.SetNotifier(notifSvc)
	alertsSvc.SetNotices(noticesSvc)
	alertsSvc.SetSources(alertSources(hostRegistry, hostTagsSvc, proxySvc))
	backupSvc.SetNotifier(notifSvc)
	backupSvc.SetNotices(noticesSvc)
	noticesSvc.Register(backup.RepairNoticeKey, func(ctx context.Context, n *notices.Service) error {
		return prepareJobTargets(ctx, database, settingsStore, backupSvc, backupTargetStore, n, func(j backup.Job, target string) {
			auditSvc.Write(ctx, "", audit.ActionBackupJobRepaired, j.Name, map[string]any{"job_id": j.ID, "target": target})
		})
	})
	// After the repair above, which seals the last plaintext credentials.
	noticesSvc.Register("db.plaintext-snapshots", func(ctx context.Context, n *notices.Service) error {
		// The snapshots are the manual way back while an update is
		// unconfirmed; the next start removes them.
		if jobGate.Pending() {
			return nil
		}
		return removePlaintextSnapshots(ctx, database, secretsSvc, n)
	})
	updateWatcher.SetNotifier(notifSvc)
	updateWatcher.Start(ctx)
	agentsSvc.SetNotifier(notifSvc)
	selfUpdateChk.SetNotifier(notifSvc)
	migrationSvc.SetNotifier(notifSvc)

	loginLimiter := handlers.NewLoginLimiter()

	restartCh := make(chan struct{}, 1)
	requestRestart := func() {
		restartRequested.Store(true)
		// Let the response reach the client before the listener closes.
		time.AfterFunc(time.Second, func() {
			select {
			case restartCh <- struct{}{}:
			default:
			}
		})
	}

	// Install-wizard commit function. Built here because it needs
	// auth.Service + settings.Store + the configured paths. It refuses an
	// install that already has an account; CreateFirstUser repeats that
	// check atomically for an account created after this precheck.
	setupCommit := func(commitCtx context.Context, in setup.CommitInput, emit func(step, msg, status string)) error {
		exists, err := setup.UsersExist(commitCtx, database)
		if err != nil {
			emit("precheck", err.Error(), "fail")
			return err
		}
		if exists {
			emit("precheck", auth.ErrAlreadySetUp.Error(), "fail")
			return auth.ErrAlreadySetUp
		}
		// Step 1 — service user. Skips entirely when we're not root,
		// because useradd needs root and the wizard usually runs as the
		// dockmesh user (non-root). The install script creates the user
		// before starting the service, so first-install operators never
		// hit this path. Only a user created right here is added to the
		// docker group: the wizard is unauthenticated, and docker group
		// membership is root-equivalent, so it never grants that to an
		// account that already exists.
		isRoot := os.Geteuid() == 0
		if in.ServiceUser.Mode == "create" {
			if !isRoot {
				emit("user.create", "skipping — wizard process is not root, install script handles user creation", "warn")
			} else {
				emit("user.create", "creating system user '"+in.ServiceUser.Username+"'", "info")
				if err := setup.CreateSystemUser(in.ServiceUser.Username, in.AddsToDockerGroup()); err != nil {
					emit("user.create", err.Error(), "fail")
					return err
				}
				emit("user.create", "system user created", "ok")
			}
		} else if check := setup.CheckSystemUser(in.ServiceUser.Username); !check.InDockerGroup {
			emit("user.docker_group", in.ServiceUser.Username+" is not in the docker group — run 'sudo usermod -aG docker "+
				in.ServiceUser.Username+"' if it needs Docker access", "warn")
		}

		// Step 2 — data directory. We only ensure existence + ownership.
		emit("data_dir", "ensuring "+in.DataDir, "info")
		if err := os.MkdirAll(in.DataDir, 0o750); err != nil {
			emit("data_dir", err.Error(), "fail")
			return err
		}
		emit("data_dir", "ready", "ok")

		// Step 3 — admin user
		emit("admin", "creating admin user '"+in.Admin.Username+"'", "info")
		commitCtx2, cancel := context.WithTimeout(commitCtx, 10*time.Second)
		defer cancel()
		if _, err := authSvc.CreateFirstUser(commitCtx2, in.Admin.Username, in.Admin.Email, in.Admin.Password, "admin"); err != nil {
			emit("admin", err.Error(), "fail")
			return err
		}
		emit("admin", "admin user created", "ok")

		// Step 4 — public URL persisted in the settings store
		if settingsStore != nil {
			emit("settings", "saving public URL "+in.PublicURL, "info")
			if err := saveSetupURLs(commitCtx2, settingsStore, in.PublicURL, cfg.AgentListen); err != nil {
				emit("settings", err.Error(), "warn")
			} else if ep, err := agentsSvc.ReloadEndpoints(); err != nil {
				emit("settings", "saved; "+err.Error(), "warn")
			} else {
				emit("settings", "saved; agents connect to "+ep.AgentURL, "ok")
			}
		}

		// Step 4.5 — drop DOCKMESH_SETUP_FORCE; with an account present it
		// is only ignored and warned about at every start.
		envPath := serverEnvFile(cfg.DBPath)
		if envPath == "" {
			if cfg.SetupForce {
				emit("setup_flag", "env file not located — remove DOCKMESH_SETUP_FORCE from it manually (default "+setup.DefaultEnvFile+")", "warn")
			}
		} else if cleaned, derr := scrubEnvFlag(envPath, "DOCKMESH_SETUP_FORCE"); derr == nil && cleaned {
			emit("setup_flag", "removed DOCKMESH_SETUP_FORCE from "+envPath, "ok")
		} else if derr != nil {
			emit("setup_flag", "could not scrub setup flag: "+derr.Error()+" (manual edit may be needed)", "warn")
		}

		// Step 5 — health probe
		emit("health", "probing /api/v1/health", "info")
		probeURL := "http://127.0.0.1" + cfg.HTTPAddr
		if strings.HasPrefix(cfg.HTTPAddr, ":") {
			probeURL = "http://127.0.0.1" + cfg.HTTPAddr
		}
		probeCtx, probeCancel := context.WithTimeout(commitCtx, 3*time.Second)
		defer probeCancel()
		req, _ := http.NewRequestWithContext(probeCtx, "GET", probeURL+"/api/v1/health", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			emit("health", "probe failed: "+err.Error(), "warn")
		} else {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				emit("health", "ok (HTTP 200)", "ok")
			} else {
				emit("health", fmt.Sprintf("HTTP %d", resp.StatusCode), "warn")
			}
		}
		return nil
	}

	noticesSvc.RunChecks(ctx)
	// Agents reconnect after the server started; look at their stacks too.
	time.AfterFunc(2*time.Minute, func() { noticesSvc.RunChecks(ctx) })

	// Wizard top-bar shows the same version `dockmesh --version` does.
	handlers.SetSetupVersionString(version.Version)

	h := handlers.New(handlers.Deps{
		DB:              database,
		Auth:            authSvc,
		Audit:           auditSvc,
		Docker:          dockerCli,
		Stacks:          stacksMgr,
		Deployments:     deployStore,
		DeployHistory:   deployHistoryStore,
		DeployTracker:   deployTracker,
		Notifications:   notifSvc,
		Notices:         noticesSvc,
		Dependencies:    depStore,
		Compose:         composeSvc,
		LoginLimiter:    loginLimiter,
		Scanner:         scannerCLI,
		ScanStore:       scanStore,
		Proxy:           proxySvc,
		Updater:         updaterSvc,
		StackLocks:      stackLocks,
		Operations:      opsSvc,
		UpdateWatcher:   updateWatcher,
		OIDC:            oidcSvc,
		SAML:            samlSvc,
		LDAP:            ldapSvc,
		OAuth2:          oauth2Svc,
		Metrics:         metricsCol,
		Notify:          notifySvc,
		Alerts:          alertsSvc,
		Backups:         backupSvc,
		BackupTargets:   backupTargetStore,
		Secrets:         secretsSvc,
		Migrations:      migrationSvc,
		Drains:          drainSvc,
		Agents:          agentsSvc,
		Hosts:           hostRegistry,
		HostTags:        hostTagsSvc,
		Roles:           rolesStore,
		Settings:        settingsStore,
		GlobalEnv:       globalEnvStore,
		APITokens:       apiTokensSvc,
		Registries:      registriesSvc,
		GitSource:       gitSourceSvc,
		Templates:       templatesSvc,
		Invites:         invitesSvc,
		AuditRetention:  auditRetention,
		AuditWebhook:    auditWebhook,
		AgentUpgrade:    agentUpgrade,
		Prom:            promMetrics,
		SelfUpdate:      selfUpdateChk,
		UpdateInstaller: updateInstaller,
		UserPrefs:       userprefs.New(database),
		Changelog:       dockmesh.Changelog,
		SetupState:      setupState,
		SetupCommit_:    setupCommit,
		RestoreConfig:   cfg,
		PublicURLs:      func() agents.Endpoints { return agentEndpoints(cfg, settingsStore) },
		RequestRestart:  requestRestart,
		FinishSetup: func() error {
			envPath := serverEnvFile(cfg.DBPath)
			if envPath == "" {
				return nil
			}
			// A root-owned config cannot be rewritten by the service; the
			// flag is inert once accounts exist and the notice names it.
			if _, err := scrubEnvFlag(envPath, "DOCKMESH_SETUP_FORCE"); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, fs.ErrPermission) {
				return err
			}
			return nil
		},
	})
	authSvc.SetAccessHooks(func(ctx context.Context) {
		if err := rolesStore.Load(ctx); err != nil {
			slog.Warn("reload roles and assignments", "err", err)
		}
	}, h.SyncIdPAssignments)
	agentsSvc.OnConnect(h.ResumePendingStackDeletes)
	for _, id := range agentsSvc.Connected() {
		go h.ResumePendingStackDeletes(ctx, id)
	}
	router := api.NewRouter(h, authSvc, webFS, cfg.MetricsAuth, setupState)

	// Backfill stack deployments (P.7): scan local containers to detect
	// which stacks are already deployed. Remote-agent containers are
	// handled lazily — agents reconnect after boot and their containers
	// will be picked up on the next deploy or via a future sync.
	if dockerCli != nil {
		go func() {
			bgCtx := context.Background()
			cli := dockerCli.Raw()
			all, err := cli.ContainerList(bgCtx, client.ContainerListOptions{All: true})
			if err != nil {
				slog.Warn("backfill: container list", "err", err)
				return
			}
			infos := make([]stacks.ContainerInfo, len(all.Items))
			for i, c := range all.Items {
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
			// The public routes use the per-IP limits of the sign-in
			// endpoints, one limiter each so downloads can't use up the
			// enrollment budget.
			agentMux.Handle(agents.EnrollPath, agents.EnrollHandler(agentsSvc, handlers.NewLoginLimiter()))
			agentMux.Handle(agents.InstallBinaryPrefix, agents.InstallHandler(agentsSvc, handlers.NewLoginLimiter()))
			agentMux.Handle(agents.BinaryPath, agents.BinaryHandler(agentsSvc))
			agentSrv := &http.Server{
				Addr:              cfg.AgentListen,
				Handler:           agentMux,
				TLSConfig:         tlsCfg,
				ReadHeaderTimeout: 10 * time.Second,
				IdleTimeout:       2 * time.Minute,
			}
			go func() {
				slog.Info("agent mtls listening", "addr", cfg.AgentListen, "public_url", agentsSvc.AgentURL())
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
	select {
	case <-stop:
	case <-restartCh:
	}

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
// target with the SFTP host-key check, opens a writer for the given
// name, and streams the archive bytes through.
type auditTargetsAdapter struct {
	store   *targets.TargetStore
	notices *notices.Service
}

func (a *auditTargetsAdapter) WriteFile(ctx context.Context, targetID int64, name string, body io.Reader) error {
	stored, err := a.store.Get(ctx, targetID)
	if err != nil {
		return err
	}
	t, err := a.store.BuildChecked(ctx, stored, a.notices)
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

// scrubEnvFlag removes any line in the env file matching `KEY=...` or
// `export KEY=...`. Returns whether the file was modified. Used by the
// wizard commit handler to drop DOCKMESH_SETUP_FORCE so the next boot
// behaves as a normal post-setup install. Idempotent — calling twice
// when the flag is already gone returns (false, nil).
func scrubEnvFlag(path, key string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(b), "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		stripped := strings.TrimPrefix(trimmed, "export ")
		if strings.HasPrefix(stripped, key+"=") {
			changed = true
			continue
		}
		out = append(out, line)
	}
	if !changed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600)
}

// saveSetupURLs stores the wizard's public URL and the agent URL derived
// from it. An agent URL someone set by hand is kept; one that still
// matches the previous base URL follows the new one.
func saveSetupURLs(ctx context.Context, st *settings.Store, publicURL, listen string) error {
	prevBase := st.Get(settings.KeyBaseURL, "")
	prevAgent := st.Get(settings.KeyAgentPublicURL, "")
	if err := st.Set(ctx, settings.KeyBaseURL, publicURL); err != nil {
		return err
	}
	if prevAgent == "" || prevAgent == agents.DeriveAgentURL(prevBase, listen) {
		return st.Set(ctx, settings.KeyAgentPublicURL, agents.DeriveAgentURL(publicURL, listen))
	}
	return nil
}

// agentEndpoints resolves the agent-facing URLs from dockmesh.env and the
// settings store. The server and the CLI commands that print install
// commands share it, so they agree on the address.
func agentEndpoints(cfg *config.Config, st *settings.Store) agents.Endpoints {
	return agents.ResolveEndpoints(agents.EndpointInputs{
		EnvBaseURL:      os.Getenv("DOCKMESH_BASE_URL"),
		EnvAgentURL:     os.Getenv("DOCKMESH_AGENT_PUBLIC_URL"),
		SettingBaseURL:  st.Get(settings.KeyBaseURL, ""),
		SettingAgentURL: st.Get(settings.KeyAgentPublicURL, ""),
		// Not cfg.BaseURL: that holds DOCKMESH_BASE_URL, which must not come
		// back as the default after it failed validation above.
		DefaultBaseURL: config.DefaultBaseURL,
		AgentListen:    cfg.AgentListen,
	})
}
