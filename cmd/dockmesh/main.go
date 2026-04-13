package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dockmesh/dockmesh/internal/api"
	"github.com/dockmesh/dockmesh/internal/api/handlers"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/oidc"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/ratelimit"
	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/updater"
	"github.com/dockmesh/dockmesh/pkg/version"
)

//go:embed all:web_dist
var webDist embed.FS

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Subcommand dispatch (§15.2: `dockmesh secrets rotate`).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "secrets":
			runSecretsCmd(os.Args[2:])
			return
		}
	}

	slog.Info("starting dockmesh", "version", version.Version, "commit", version.Commit)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
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

	proxySvc := proxy.NewService(database, dockerCli, cfg.ProxyEnabled)
	if cfg.ProxyEnabled {
		if err := proxySvc.SyncFromDB(ctx); err != nil {
			slog.Warn("proxy sync failed — caddy container may not be running yet", "err", err)
		}
	}
	updaterSvc := updater.NewService(dockerCli, database)
	oidcSvc := oidc.NewService(database, authSvc, secretsSvc, cfg.BaseURL)

	metricsCol := metrics.NewCollector(database, dockerCli, 30*time.Second, metrics.DefaultRetention)
	metricsCol.Start(ctx)
	defer metricsCol.Stop()

	loginLimiter := ratelimit.New(10, time.Minute, 5*time.Minute)
	h := handlers.New(handlers.Deps{
		DB:           database,
		Auth:         authSvc,
		Audit:        auditSvc,
		Docker:       dockerCli,
		Stacks:       stacksMgr,
		Compose:      composeSvc,
		LoginLimiter: loginLimiter,
		Scanner:      scannerSvc,
		ScanStore:    scanStore,
		Proxy:        proxySvc,
		Updater:      updaterSvc,
		OIDC:         oidcSvc,
		Metrics:      metricsCol,
		JWTSecret:    cfg.JWTSecret,
	})
	router := api.NewRouter(h, authSvc, webFS)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
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

	slog.Info("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}
