package proxy

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// nextContainerName holds the new container during the move to the socket,
// while the old one still owns the fixed name for the way back.
const nextContainerName = ProxyContainerName + "-next"

const (
	noticeKeyAdminSwitch = "proxy.admin-socket-switch-failed"
	autosavePath         = "/config/caddy/autosave.json"
	maxAutosaveTar       = 16 << 20
)

// ErrAdminSwitchFailed means the proxy's admin API couldn't be moved to
// the unix socket; the old container keeps serving when it can.
var ErrAdminSwitchFailed = errors.New("moving the proxy's admin API to the unix socket failed")

// containerAPI is the part of the Docker client the move to the socket
// uses, so tests can drive its failure paths.
type containerAPI interface {
	ContainerInspect(ctx context.Context, id string, opts client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(ctx context.Context, id string, opts client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerRename(ctx context.Context, id string, opts client.ContainerRenameOptions) (client.ContainerRenameResult, error)
	CopyToContainer(ctx context.Context, id string, opts client.CopyToContainerOptions) (client.CopyToContainerResult, error)
	CopyFromContainer(ctx context.Context, id string, opts client.CopyFromContainerOptions) (client.CopyFromContainerResult, error)
	VolumeCreate(ctx context.Context, opts client.VolumeCreateOptions) (client.VolumeCreateResult, error)
}

// SetAdminSocketDir sets the data directory the admin socket lives under.
func (s *Service) SetAdminSocketDir(dataDir string) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		abs = dataDir
	}
	s.adminSocketDir = filepath.Join(abs, AdminSocketDirName)
	s.useSocketAdmin()
}

// SetInContainer sets the check for Dockmesh itself running in a
// container, where the socket directory's path means something else to
// the Docker daemon.
func (s *Service) SetInContainer(check func() bool) { s.inContainer = check }

// containerMarkers are files the container runtimes create in every
// container; unlike asking the daemon, reading them can't fail open.
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// DetectContainer returns the check for SetInContainer. selfID is the
// daemon's answer, which may arrive only after startup.
func DetectContainer(selfID func() string) func() bool {
	return func() bool {
		if selfID() != "" {
			return true
		}
		for _, f := range containerMarkers {
			if _, err := os.Stat(f); err == nil {
				return true
			}
		}
		return false
	}
}

func (s *Service) adminSocketPath() string {
	return filepath.Join(s.adminSocketDir, adminSocketName)
}

func (s *Service) useSocketAdmin() {
	adminEP.Store(&adminEndpoint{url: "http://" + adminHost, socket: s.adminSocketPath()})
	s.legacyAdmin.Store(false)
}

func (s *Service) useLegacyAdmin() {
	adminEP.Store(legacyEP)
	s.legacyAdmin.Store(true)
}

func (s *Service) socketMissing() bool {
	_, err := os.Lstat(s.adminSocketPath())
	return errors.Is(err, os.ErrNotExist)
}

// ensureSocketDir creates the admin socket's directory, private to
// Dockmesh's user, and refuses one it can't trust to be private.
func (s *Service) ensureSocketDir() error {
	dir := s.adminSocketDir
	if dir == "" {
		return errors.New("the admin socket directory is not set")
	}
	if s.inContainer != nil && s.inContainer() {
		return errors.New("the reverse proxy needs Dockmesh installed directly on the host: from inside a container the admin socket can't be shared with the proxy")
	}
	if strings.Contains(dir, ":") {
		return fmt.Errorf("the data directory %s contains a colon, which Docker can't mount", filepath.Dir(dir))
	}
	if len(s.adminSocketPath()) > maxSocketPath {
		return fmt.Errorf("the data directory %s is too long for a unix socket path (at most %d characters in total)", filepath.Dir(dir), maxSocketPath)
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create admin socket directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("check admin socket directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if !ownedByMe(info) {
		return fmt.Errorf("%s is not owned by the user Dockmesh runs as; remove it and restart Dockmesh", dir)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("restrict admin socket directory: %w", err)
		}
	}
	return nil
}

// usesTCPAdmin reports whether c is shaped like a managed container from
// before the admin API moved to the socket. Labels alone wouldn't do:
// anyone who can create a container can copy them.
func usesTCPAdmin(c *container.InspectResponse) bool {
	if c.Config == nil || c.HostConfig == nil {
		return false
	}
	return c.Config.Labels["dockmesh.component"] == "proxy" && hasProxyShape(c, proxyVolumes)
}

// isOldManagedContainer adds to usesTCPAdmin that the container runs a
// Caddy image Dockmesh pins, so its admin API is trusted with the routes.
// Anything else is replaced like any other outdated container.
func (s *Service) isOldManagedContainer(ctx context.Context, c *container.InspectResponse) bool {
	if !usesTCPAdmin(c) {
		return false
	}
	ref := c.Config.Image
	if ref != s.currentImage() && versionOfImage(ref) == "" {
		return false
	}
	if s.imageID != nil {
		return c.Image == s.imageID(ctx, ref)
	}
	inspected, err := s.docker.Raw().ImageInspect(ctx, ref)
	return err == nil && c.Image == inspected.ID
}

// adoptNextContainer finishes or discards a move that was interrupted:
// with the old container gone the new one takes the fixed name, otherwise
// the new one is dropped and the move starts over.
func (s *Service) adoptNextContainer(ctx context.Context, cli containerAPI) error {
	res, err := cli.ContainerInspect(ctx, nextContainerName, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", nextContainerName, err)
	}
	next := res.Container
	if next.Config == nil || next.Config.Labels["dockmesh.component"] != "proxy" ||
		next.Config.Labels[specLabel] != s.specHashFor(next.Config.Image) {
		return nil
	}
	_, err = cli.ContainerInspect(ctx, ProxyContainerName, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		_, err = cli.ContainerRename(ctx, next.ID, client.ContainerRenameOptions{NewName: ProxyContainerName})
		return err
	}
	if err != nil {
		return fmt.Errorf("inspect proxy container: %w", err)
	}
	_, err = cli.ContainerRemove(ctx, next.ID, client.ContainerRemoveOptions{Force: true})
	return err
}

// moveAdminToSocket replaces a container whose admin API is on TCP and
// keeps the admin notice in line with the outcome.
func (s *Service) moveAdminToSocket(ctx context.Context, cli containerAPI, img string, old *container.InspectResponse) error {
	if err := s.tryMoveAdmin(ctx, cli, img, old); err != nil {
		slog.Warn("proxy: moving the admin API to the unix socket failed", "err", err)
		s.setAdminSwitchNotice(ctx, err)
		return fmt.Errorf("%w: %w", ErrAdminSwitchFailed, err)
	}
	s.setAdminSwitchNotice(ctx, nil)
	slog.Info("proxy: admin API moved to the unix socket")
	return nil
}

// tryMoveAdmin stops the old container only once everything that can fail
// without touching it is done, because just one Caddy can hold :80 and
// :443, and starts it again when the new one doesn't take the config.
func (s *Service) tryMoveAdmin(ctx context.Context, cli containerAPI, img string, old *container.InspectResponse) error {
	if err := s.ensureSocketDir(); err != nil {
		return s.keepOld(ctx, old, err)
	}
	if err := s.ensureImage(ctx, img); err != nil {
		return s.keepOld(ctx, old, err)
	}
	autosave, err := readAutosave(ctx, cli, old.ID)
	if err == nil && !isOldAutosave(autosave, old) {
		err = errors.New("the saved config is not the old container's")
	}
	if err != nil {
		if old.State != nil && old.State.Running {
			return s.keepOld(ctx, old, fmt.Errorf("save the running config: %w", err))
		}
		// An interrupted move already replaced the file and the proxy is
		// down, so going forward is the only way to get it serving.
		autosave = nil
	}
	id, adminID, err := s.createContainer(ctx, cli, img, nextContainerName)
	if cerrdefs.IsConflict(err) {
		err = fmt.Errorf("a container named %s is in the way; remove it: %w", nextContainerName, err)
	}
	if err != nil {
		return s.keepOld(ctx, old, err)
	}

	// Past this point a cancelled request or shutdown must not leave the
	// proxy stopped halfway.
	moveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), switchTimeout)
	defer cancel()
	if _, err := cli.ContainerStop(moveCtx, old.ID, client.ContainerStopOptions{}); err != nil {
		_, _ = cli.ContainerRemove(moveCtx, id, client.ContainerRemoveOptions{Force: true})
		return s.keepOld(ctx, old, fmt.Errorf("stop the old container: %w", err))
	}
	if err := s.startOnSocket(moveCtx, cli, id, adminID); err != nil {
		if rbErr := s.restoreOld(moveCtx, cli, old, id, autosave); rbErr != nil {
			s.enabled.Store(false)
			return fmt.Errorf("%w; starting the old container again also failed: %w", err, rbErr)
		}
		return err
	}
	if _, err := cli.ContainerRemove(moveCtx, old.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		slog.Warn("proxy: removing the old container failed", "err", err)
	}
	if _, err := cli.ContainerRename(moveCtx, id, client.ContainerRenameOptions{NewName: ProxyContainerName}); err != nil {
		slog.Warn("proxy: renaming the new container failed", "err", err)
	}
	return nil
}

// keepOld records that the old container was left untouched and keeps
// managing it over TCP. A container that isn't serving is left to the
// watchdog's retries.
func (s *Service) keepOld(ctx context.Context, old *container.InspectResponse, cause error) error {
	if old.State != nil && old.State.Running {
		s.manageOld(ctx, old)
	}
	return cause
}

// manageOld points Dockmesh at the old container's TCP admin API.
func (s *Service) manageOld(ctx context.Context, old *container.InspectResponse) {
	s.useLegacyAdmin()
	id := old.Config.Labels[adminIDLabel]
	if !validAdminID(id) {
		s.adminID.Store(nil)
		s.enabled.Store(true)
		return
	}
	if err := ensureAdmin(ctx, id); err != nil {
		slog.Warn("proxy: the old container's admin API doesn't answer", "err", err)
	}
	s.adminID.Store(&id)
	s.enabled.Store(true)
	if err := s.SyncFromDB(ctx); err != nil {
		slog.Warn("proxy: pushing the routes to the old container failed", "err", err)
	}
}

func (s *Service) startOnSocket(ctx context.Context, cli containerAPI, id, adminID string) error {
	if err := seedAndStart(ctx, cli, id, adminID); err != nil {
		return err
	}
	s.useSocketAdmin()
	if err := ensureAdmin(ctx, adminID); err != nil {
		return err
	}
	s.adminID.Store(&adminID)
	s.enabled.Store(true)
	if err := s.SyncFromDB(ctx); err != nil {
		// Caddy's error can quote other sites' configuration, and the
		// cause ends up in a notice.
		slog.Warn("proxy: the new container refused the config", "err", err)
		return ErrConfigRejected
	}
	return nil
}

func (s *Service) restoreOld(ctx context.Context, cli containerAPI, old *container.InspectResponse, newID string, autosave []byte) error {
	if _, err := cli.ContainerRemove(ctx, newID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove the new container: %w", err)
	}
	if autosave == nil {
		return errors.New("no saved config of the old container to go back to")
	}
	if _, err := cli.CopyToContainer(ctx, old.ID, client.CopyToContainerOptions{DestinationPath: "/config/caddy", Content: bytes.NewReader(autosave)}); err != nil {
		return fmt.Errorf("restore the saved config: %w", err)
	}
	if _, err := cli.ContainerStart(ctx, old.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start the old container: %w", err)
	}
	s.manageOld(ctx, old)
	return nil
}

// readAutosave returns Caddy's saved config as the tar stream Docker
// produced, ready to be copied back.
func readAutosave(ctx context.Context, cli containerAPI, id string) ([]byte, error) {
	res, err := cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: autosavePath})
	if err != nil {
		return nil, err
	}
	defer res.Content.Close()
	b, err := io.ReadAll(io.LimitReader(res.Content, maxAutosaveTar+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxAutosaveTar {
		return nil, fmt.Errorf("%s is larger than %d bytes", autosavePath, maxAutosaveTar)
	}
	return b, nil
}

// isOldAutosave reports whether the tar from readAutosave holds a config
// of the old container. The config volume is shared, so after an
// interrupted move it can hold the new container's config, which the old
// one couldn't start from.
func isOldAutosave(archive []byte, old *container.InspectResponse) bool {
	tr := tar.NewReader(bytes.NewReader(archive))
	if _, err := tr.Next(); err != nil {
		return false
	}
	var cfg struct {
		Admin struct {
			ID     string `json:"@id"`
			Listen string `json:"listen"`
		} `json:"admin"`
	}
	if err := json.NewDecoder(tr).Decode(&cfg); err != nil || cfg.Admin.Listen != legacyAdminAddr {
		return false
	}
	id := old.Config.Labels[adminIDLabel]
	return !validAdminID(id) || cfg.Admin.ID == id
}

func (s *Service) setAdminSwitchNotice(ctx context.Context, cause error) {
	if s.notices == nil {
		return
	}
	in := notices.RaiseInput{Key: noticeKeyAdminSwitch, Audience: notices.Admins(), Severity: notices.SevWarning, Link: "/proxy"}
	if cause != nil {
		in.Title = i18n.Msg("notice.proxy_admin_switch.title")
		in.Body = i18n.Msg("notice.proxy_admin_switch.body", cause.Error(), legacyAdminAddr)
	}
	if err := s.notices.Set(context.WithoutCancel(ctx), cause != nil, in); err != nil {
		slog.Warn("proxy: update admin socket notice failed", "err", err)
	}
}
