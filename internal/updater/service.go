// Package updater handles one-click container image updates with
// automatic rollback snapshots (concept §2.2). The flow per update:
//
//  1. Pull the latest version of the container's image ref.
//  2. If the local image id is unchanged, return "already up to date".
//  3. Otherwise tag the *old* image as <ref>-rollback-<unix> so it
//     survives the next prune.
//  4. Stop + remove the old container.
//  5. Recreate a new container with the exact same Config/HostConfig/
//     Networks but the new image.
//  6. Record the change in update_history so the user can rollback.
//
// Rollback uses the stored rollback tag as the image ref and runs the
// same stop/remove/recreate flow.
package updater

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/docker"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dnetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

var (
	ErrDockerUnavailable = errors.New("docker unavailable")
	ErrAlreadyRolledBack = errors.New("already rolled back")
	ErrHistoryNotFound   = errors.New("history entry not found")
)

type Service struct {
	docker *docker.Client
	db     *sql.DB
}

func NewService(dockerCli *docker.Client, db *sql.DB) *Service {
	return &Service{docker: dockerCli, db: db}
}

type Result struct {
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
	Image         string `json:"image"`
	OldDigest     string `json:"old_digest"`
	NewDigest     string `json:"new_digest"`
	Updated       bool   `json:"updated"`
	RollbackTag   string `json:"rollback_tag,omitempty"`
	HistoryID     int64  `json:"history_id,omitempty"`
}

type Entry struct {
	ID            int64      `json:"id"`
	ContainerName string     `json:"container_name"`
	ImageRef      string     `json:"image_ref"`
	OldDigest     string     `json:"old_digest"`
	NewDigest     string     `json:"new_digest"`
	RollbackTag   string     `json:"rollback_tag"`
	AppliedAt     time.Time  `json:"applied_at"`
	RolledBackAt  *time.Time `json:"rolled_back_at,omitempty"`
}

// Update pulls the latest image for the container and, if it differs,
// snapshots the old image and recreates the container.
func (s *Service) Update(ctx context.Context, containerID string) (*Result, error) {
	if s.docker == nil {
		return nil, ErrDockerUnavailable
	}
	cli := s.docker.Raw()

	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect: %w", err)
	}
	ref := normalizeImageRef(info.Config.Image)
	containerName := strings.TrimPrefix(info.Name, "/")
	oldImageID := info.Image

	// Pull latest.
	rc, err := cli.ImagePull(ctx, ref, dtypes.ImagePullOptions{})
	if err != nil {
		return nil, fmt.Errorf("pull %s: %w", ref, err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		rc.Close()
		return nil, err
	}
	rc.Close()

	newInfo, _, err := cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("inspect new image: %w", err)
	}
	newImageID := newInfo.ID

	res := &Result{
		ContainerID:   containerID,
		ContainerName: containerName,
		Image:         ref,
		OldDigest:     oldImageID,
		NewDigest:     newImageID,
	}
	if oldImageID == newImageID {
		return res, nil
	}

	// Snapshot the *old* image before the new one overwrites the tag.
	rollbackTag := fmt.Sprintf("%s-rollback-%d", strings.ReplaceAll(ref, ":", "-"), time.Now().Unix())
	// Format must be repo:tag. Split: <repo>:<tag>
	// Fallback to a flat repo if splitting fails.
	sanitized := sanitizeTag(ref, time.Now().Unix())
	rollbackTag = sanitized
	if err := cli.ImageTag(ctx, oldImageID, rollbackTag); err != nil {
		return nil, fmt.Errorf("tag rollback image: %w", err)
	}

	newContainerID, err := recreateContainer(ctx, cli, &info, ref)
	if err != nil {
		// Best-effort cleanup of the rollback tag if recreate failed.
		_, _ = cli.ImageRemove(ctx, rollbackTag, dtypes.ImageRemoveOptions{})
		return nil, fmt.Errorf("recreate: %w", err)
	}

	// Persist history.
	exec, err := s.db.ExecContext(ctx, `
		INSERT INTO update_history (container_name, image_ref, old_digest, new_digest, rollback_tag)
		VALUES (?, ?, ?, ?, ?)
	`, containerName, ref, oldImageID, newImageID, rollbackTag)
	if err != nil {
		slog.Warn("update history insert failed", "err", err)
	} else if id, err := exec.LastInsertId(); err == nil {
		res.HistoryID = id
	}
	res.ContainerID = newContainerID
	res.RollbackTag = rollbackTag
	res.Updated = true
	return res, nil
}

// Rollback reverts the container to the image stored in the given history
// entry. The container is recreated with the same Config/HostConfig.
func (s *Service) Rollback(ctx context.Context, historyID int64) (*Result, error) {
	if s.docker == nil {
		return nil, ErrDockerUnavailable
	}
	var e Entry
	var rolledAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, container_name, image_ref, old_digest, new_digest, rollback_tag, applied_at, rolled_back_at
		FROM update_history WHERE id = ?`, historyID).
		Scan(&e.ID, &e.ContainerName, &e.ImageRef, &e.OldDigest, &e.NewDigest, &e.RollbackTag, &e.AppliedAt, &rolledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHistoryNotFound
	}
	if err != nil {
		return nil, err
	}
	if rolledAt.Valid {
		return nil, ErrAlreadyRolledBack
	}

	cli := s.docker.Raw()

	// Find the current container by name.
	info, err := cli.ContainerInspect(ctx, e.ContainerName)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", e.ContainerName, err)
	}

	// Recreate with the rollback tag as the image.
	newID, err := recreateContainer(ctx, cli, &info, e.RollbackTag)
	if err != nil {
		return nil, fmt.Errorf("recreate: %w", err)
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE update_history SET rolled_back_at = CURRENT_TIMESTAMP WHERE id = ?`, historyID); err != nil {
		slog.Warn("update history rollback update failed", "err", err)
	}

	return &Result{
		ContainerID:   newID,
		ContainerName: e.ContainerName,
		Image:         e.RollbackTag,
		OldDigest:     e.NewDigest,
		NewDigest:     e.OldDigest,
		Updated:       true,
		HistoryID:     historyID,
	}, nil
}

// History returns the update history for a single container, newest first.
func (s *Service) History(ctx context.Context, containerName string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, container_name, image_ref, old_digest, new_digest, rollback_tag, applied_at, rolled_back_at
		FROM update_history
		WHERE container_name = ?
		ORDER BY id DESC
		LIMIT 50
	`, containerName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var rolledAt sql.NullTime
		if err := rows.Scan(&e.ID, &e.ContainerName, &e.ImageRef, &e.OldDigest, &e.NewDigest, &e.RollbackTag, &e.AppliedAt, &rolledAt); err != nil {
			return nil, err
		}
		if rolledAt.Valid {
			t := rolledAt.Time
			e.RolledBackAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// recreateContainer stops + removes the old container and creates a fresh
// one with the same Config/HostConfig/Networks but a new image ref.
func recreateContainer(ctx context.Context, cli *client.Client, info *dtypes.ContainerJSON, imageRef string) (string, error) {
	cfg := *info.Config
	cfg.Image = imageRef
	hostCfg := info.HostConfig

	netCfg := &dnetwork.NetworkingConfig{}
	if info.NetworkSettings != nil && len(info.NetworkSettings.Networks) > 0 {
		netCfg.EndpointsConfig = info.NetworkSettings.Networks
	}
	name := strings.TrimPrefix(info.Name, "/")

	_ = cli.ContainerStop(ctx, info.ID, container.StopOptions{})
	if err := cli.ContainerRemove(ctx, info.ID, container.RemoveOptions{Force: true}); err != nil {
		return "", fmt.Errorf("remove old: %w", err)
	}

	resp, err := cli.ContainerCreate(ctx, &cfg, hostCfg, netCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}
	return resp.ID, nil
}

func normalizeImageRef(ref string) string {
	// If the ref has no tag, docker treats it as :latest.
	// Only check the final path segment so `localhost:5000/foo` doesn't
	// get misread as `localhost` + tag `5000/foo`.
	lastSlash := strings.LastIndex(ref, "/")
	tagPart := ref[lastSlash+1:]
	if !strings.Contains(tagPart, ":") {
		return ref + ":latest"
	}
	return ref
}

// sanitizeTag builds a rollback tag in the form <repo>:<original-tag>-rollback-<ts>.
// It handles refs with registry prefixes and falls back to a safe default.
func sanitizeTag(ref string, ts int64) string {
	// Split last colon as the tag.
	lastSlash := strings.LastIndex(ref, "/")
	tagPart := ref[lastSlash+1:]
	colon := strings.LastIndex(tagPart, ":")
	if colon < 0 {
		return ref + fmt.Sprintf(":rollback-%d", ts)
	}
	repo := ref[:lastSlash+1+colon]
	tag := tagPart[colon+1:]
	return fmt.Sprintf("%s:%s-rollback-%d", repo, tag, ts)
}
