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
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/config"
	"github.com/dockmesh/dockmesh/internal/db"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/pkg/version"
)

//go:embed all:web_dist
var webDist embed.FS

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

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

	stacksMgr, err := stacks.NewManager(cfg.StacksRoot)
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
	h := handlers.New(database, authSvc, dockerCli, stacksMgr, composeSvc)
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
