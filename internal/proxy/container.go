package proxy

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dnetwork "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// bootstrapConfig is the minimal Caddy config we seed into the config
// volume before first start. It only enables the admin API; the real
// config is pushed via /load once the API is reachable.
var bootstrapConfig = []byte(`{"admin":{"listen":"` + adminAddr + `"}}`)

// ProxyContainerName is the fixed name we use for the managed Caddy
// container so repeated enable/disable cycles don't leave orphans.
const ProxyContainerName = "dockmesh-proxy"

// DefaultProxyImage pins the Caddy release Dockmesh is tested with.
const DefaultProxyImage = "caddy:2.11.7"

// specLabel carries a hash of the container spec, so reconcile can tell
// a container it can keep from one created with an older spec.
const specLabel = "dockmesh.proxy.spec"

// proxyCmd starts Caddy from its autosaved last config when there is
// one (every /load is saved), so a restarted container keeps serving
// the routes without waiting for Dockmesh.
var proxyCmd = []string{"caddy", "run", "--config", "/config/caddy.json", "--resume"}

const (
	proxyNetworkMode   = "host"
	proxyRestartPolicy = "unless-stopped"
)

var proxyBinds = []string{
	"dockmesh-caddy-data:/data",
	"dockmesh-caddy-config:/config",
}

func (s *Service) specHash() string {
	h := sha256.New()
	for _, part := range [][]string{{s.image}, proxyCmd, proxyBinds, {proxyNetworkMode, proxyRestartPolicy}} {
		for _, p := range part {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// EnableProxy makes sure the managed Caddy container runs with the
// current spec and serves the current routes. A container that already
// matches the spec is kept (and started if stopped); only an outdated
// or foreign one is replaced. Called at boot and by the enable toggle.
func (s *Service) EnableProxy(ctx context.Context) error {
	if s.docker == nil {
		return errors.New("docker unavailable")
	}
	cli := s.docker.Raw()
	want := s.specHash()

	existing, err := cli.ContainerInspect(ctx, ProxyContainerName)
	switch {
	case err == nil && existing.Config != nil && existing.Config.Labels[specLabel] == want:
		if existing.State == nil || !existing.State.Running {
			if err := cli.ContainerStart(ctx, existing.ID, container.StartOptions{}); err != nil {
				return fmt.Errorf("container start: %w", err)
			}
		}
	case err == nil:
		slog.Info("proxy: replacing container with outdated spec", "container", existing.ID[:12])
		if err := s.createProxy(ctx, &existing); err != nil {
			return err
		}
	case errdefs.IsNotFound(err):
		if err := s.createProxy(ctx, nil); err != nil {
			return err
		}
	default:
		return fmt.Errorf("inspect proxy container: %w", err)
	}

	// Host networking means 127.0.0.1:2019 is Caddy's.
	if err := ensureAdmin(ctx); err != nil {
		return err
	}
	s.enabled.Store(true)
	return s.SyncFromDB(ctx)
}

// createProxy pulls the image if needed, removes old (when set) and
// creates + starts a fresh container.
func (s *Service) createProxy(ctx context.Context, old *dtypes.ContainerJSON) error {
	cli := s.docker.Raw()
	if _, _, err := cli.ImageInspectWithRaw(ctx, s.image); err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("image inspect: %w", err)
		}
		rc, err := cli.ImagePull(ctx, s.image, dtypes.ImagePullOptions{})
		if err != nil {
			return fmt.Errorf("image pull %s: %w", s.image, err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			rc.Close()
			return fmt.Errorf("image pull read: %w", err)
		}
		rc.Close()
	}

	if old != nil {
		_ = cli.ContainerStop(ctx, old.ID, container.StopOptions{})
		if err := cli.ContainerRemove(ctx, old.ID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("remove outdated proxy container: %w", err)
		}
	}

	cfg := &container.Config{
		Image: s.image,
		Cmd:   proxyCmd,
		Labels: map[string]string{
			"dockmesh.managed":   "true",
			"dockmesh.component": "proxy",
			specLabel:            s.specHash(),
		},
	}
	hostCfg := &container.HostConfig{
		NetworkMode:   proxyNetworkMode,
		RestartPolicy: container.RestartPolicy{Name: proxyRestartPolicy},
		Binds:         proxyBinds,
	}
	resp, err := cli.ContainerCreate(ctx, cfg, hostCfg, &dnetwork.NetworkingConfig{}, nil, ProxyContainerName)
	if err != nil {
		return fmt.Errorf("container create: %w", err)
	}

	// Seed the bootstrap config so caddy has something to read on its
	// very first start. Copied after Create and before Start so there's
	// no race with the Caddy process reading the file.
	tarStream, err := tarSingleFile("caddy.json", bootstrapConfig)
	if err != nil {
		return fmt.Errorf("build bootstrap tar: %w", err)
	}
	if err := cli.CopyToContainer(ctx, resp.ID, "/config", tarStream, dtypes.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("seed bootstrap config: %w", err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("container start: %w", err)
	}
	return nil
}

// StartWatchdog re-pushes the routes whenever Caddy runs a different
// config than the stored routes produce, e.g. after the container was
// recreated outside Dockmesh or its autosave was lost.
func (s *Service) StartWatchdog(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if err := s.reconcileConfig(ctx); err != nil {
				slog.Debug("proxy: config check failed", "err", err)
			}
		}
	}()
}

func (s *Service) reconcileConfig(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	running, err := runningConfig(ctx)
	if err != nil {
		return err
	}
	want, err := s.desiredConfig(ctx)
	if err != nil {
		return err
	}
	if sameConfig(running, want) {
		return nil
	}
	slog.Info("proxy: caddy config differs from stored routes, pushing")
	return pushConfig(ctx, want)
}

// DisableProxy stops and removes the managed Caddy container. The Docker
// volumes (dockmesh-caddy-data, dockmesh-caddy-config) are left intact so
// reissued certificates survive the disable/enable cycle.
func (s *Service) DisableProxy(ctx context.Context) error {
	if s.docker == nil {
		return errors.New("docker unavailable")
	}
	cli := s.docker.Raw()
	info, err := cli.ContainerInspect(ctx, ProxyContainerName)
	if err != nil {
		if errdefs.IsNotFound(err) {
			s.enabled.Store(false)
			return nil
		}
		return err
	}
	_ = cli.ContainerStop(ctx, info.ID, container.StopOptions{})
	if err := cli.ContainerRemove(ctx, info.ID, container.RemoveOptions{Force: true}); err != nil {
		return err
	}
	s.enabled.Store(false)
	return nil
}

// tarSingleFile wraps a single in-memory file as a tar stream suitable for
// Docker's CopyToContainer.
func tarSingleFile(name string, content []byte) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}

// GetStatus inspects the container and pings the admin API.
func (s *Service) GetStatus(ctx context.Context) *Status {
	st := &Status{Enabled: s.Enabled()}
	if s.docker == nil {
		return st
	}
	cli := s.docker.Raw()
	info, err := cli.ContainerInspect(ctx, ProxyContainerName)
	if err != nil {
		return st
	}
	st.Container = info.ID[:12]
	st.Running = info.State != nil && info.State.Running
	if st.Running {
		ok, server := adminStatus(ctx)
		st.AdminOK = ok
		if server != "" {
			st.Version = server
		}
	}
	return st
}
