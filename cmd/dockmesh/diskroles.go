package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/system"
)

// serverRolePaths labels the filesystems holding Docker, containerd,
// the stacks dir, the data dir and local backup targets.
func serverRolePaths(database *sql.DB, cli *docker.Client, stacksRoot, dataDir string) system.RolePathsFn {
	return func(ctx context.Context) []system.RolePath {
		paths := []system.RolePath{
			{Role: system.RoleContainerdRoot, Path: system.ContainerdRoot()},
			{Role: system.RoleStacks, Path: stacksRoot},
			{Role: system.RoleData, Path: dataDir},
		}
		if cli != nil && cli.Connected() {
			ictx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if info, err := cli.Info(ictx); err == nil && info.DockerRootDir != "" {
				paths = append(paths, system.RolePath{Role: system.RoleDockerRoot, Path: info.DockerRootDir})
			}
		}
		for _, p := range localBackupPaths(ctx, database) {
			paths = append(paths, system.RolePath{Role: system.RoleBackupTarget, Path: p})
		}
		return paths
	}
}

func localBackupPaths(ctx context.Context, database *sql.DB) []string {
	rows, err := database.QueryContext(ctx, `
		SELECT target_config FROM backup_jobs WHERE target_type = 'local'
		UNION
		SELECT config_json FROM backup_targets WHERE type = 'local'`)
	if err != nil {
		slog.Debug("disk roles: list local backup targets", "err", err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return out
		}
		var cfg struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(raw), &cfg) == nil && cfg.Path != "" {
			out = append(out, cfg.Path)
		}
	}
	return out
}
