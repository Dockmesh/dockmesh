package compose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dnetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// composeTracer is the tracer for every compose-phase span. Global
// no-op provider when tracing is disabled, so calling Start is cheap.
var composeTracer = otel.Tracer("dockmesh.compose")

// Project labels applied to every resource we create so Stop/Status
// can find them back without a DB entry.
const (
	LabelProject = "com.docker.compose.project"
	LabelService = "com.docker.compose.service"
	LabelManaged = docker.LabelManaged
)

// authResolver is what compose calls before every image pull so the
// daemon API gets a proper X-Registry-Auth blob instead of an anonymous
// request; registries.Service.ForHost returns one bound to a host.
type authResolver interface {
	ResolveAuthForImage(ctx context.Context, image string) (string, error)
}

// progressTracker is what compose updates as it walks through a
// deploy. Kept as an interface so the deploy package doesn't pull
// every consumer (api/handlers, gitsource) into compose's import
// graph. Nil = no live progress (legacy behaviour). Phase is a plain
// string so the deploy package can define its own typed constants
// without imposing them on compose.
type progressTracker interface {
	Begin(stack string, totalServices int)
	SetPhase(stack string, phase string, service, image string, step int)
	Finish(stack string, err error)
}

const (
	progressPulling    = "pulling"
	progressCreating   = "creating"
	progressStarting   = "starting_container"
	progressHealthwait = "healthcheck"
)

// Service wires a Docker client and stack manager into compose operations.
//
// Supported compose features:
//   - services: image, environment, command, entrypoint, ports, volumes,
//     networks, restart, labels, user, working_dir, hostname, privileged,
//     read_only, cap_add, cap_drop, tty, stdin_open, healthcheck
//   - top-level networks/volumes (named, non-external → auto-create; external
//     assumed to exist)
//   - .env file resolution
//
// Not supported (ignored or refused with an error):
//   - build:, secrets:, configs:, depends_on ordering, deploy: (a
//     deploy.replicas other than 1 is reported in DeployResult.ReplicasIgnored),
//     profiles, extends, network aliases, ipam overrides
//
// WaitHealthy runs after ContainerStart so a failing probe surfaces at
// deploy time rather than silently in docker ps.
type Service struct {
	docker  *docker.Client
	stacks  *stacks.Manager
	auth    authResolver
	tracker progressTracker
}

func NewService(d *docker.Client, s *stacks.Manager) *Service {
	return &Service{docker: d, stacks: s}
}

// SetAuthResolver wires the registries service in post-construction so
// main.go's ordering stays simple. Nil disables auth → pulls fall back
// to anonymous, same as before.
func (s *Service) SetAuthResolver(r authResolver) { s.auth = r }

// SetProgressTracker wires the deploy.Tracker so the UI gets live
// phase + service + elapsed updates during a deploy. Nil is safe — all
// tracker calls are guarded by the package-level helper.
func (s *Service) SetProgressTracker(t progressTracker) { s.tracker = t }

// forcePullCtxKey is a private type so callers can only set it via
// WithForcePull. The value indicates "ignore the local-cache check on
// every image in this deploy and pull from the registry instead".
type forcePullCtxKey struct{}

// WithForcePull marks the context so the deploy executor pulls every
// image regardless of whether it's already cached locally. Used by
// the git-source auto-deploy path so a fresh push to GHCR is actually
// picked up even when compose.yaml didn't move.
func WithForcePull(ctx context.Context) context.Context {
	return context.WithValue(ctx, forcePullCtxKey{}, true)
}

// ForcePullRequested reports whether ctx came from WithForcePull.
func ForcePullRequested(ctx context.Context) bool {
	v, _ := ctx.Value(forcePullCtxKey{}).(bool)
	return v
}

type registryAuthCtxKey struct{}

// WithRegistryAuth attaches X-Registry-Auth blobs keyed by image
// reference. They win over the service's resolver; agents get them with
// each request because they can't read the server's registry list.
func WithRegistryAuth(ctx context.Context, auth map[string]string) context.Context {
	if len(auth) == 0 {
		return ctx
	}
	return context.WithValue(ctx, registryAuthCtxKey{}, auth)
}

// resolveImageAuth returns the base64 X-Registry-Auth blob for an image,
// or empty string when no matching registry credential exists (anonymous
// pull). Logs the resolution
// outcome so a 401 on the actual pull can be triaged against whether
// we even sent credentials.
func (s *Service) resolveImageAuth(ctx context.Context, image string) string {
	if auth, ok := ctx.Value(registryAuthCtxKey{}).(map[string]string); ok && auth[image] != "" {
		slog.Info("image pull: using registry credential", "image", image, "source", "request", "auth_bytes", len(auth[image]))
		return auth[image]
	}
	if s.auth == nil {
		slog.Debug("image pull: no auth resolver wired", "image", image)
		return ""
	}
	auth, err := s.auth.ResolveAuthForImage(ctx, image)
	if err != nil {
		slog.Warn("image pull: auth resolve error", "image", image, "err", err)
		return ""
	}
	if auth == "" {
		slog.Info("image pull: no matching registry credential, falling back to anonymous",
			"image", image)
	} else {
		slog.Info("image pull: using registry credential",
			"image", image, "auth_bytes", len(auth))
	}
	return auth
}

type DeployResult struct {
	Stack    string           `json:"stack"`
	Services []ServiceResult  `json:"services"`
	Networks []ResourceResult `json:"networks,omitempty"`
	Volumes  []ResourceResult `json:"volumes,omitempty"`
	// BindCopies lists data moved out of old staging bind mounts.
	BindCopies []BindCopy `json:"bind_copies,omitempty"`
	// ReplicasIgnored lists services whose deploy.replicas asks for a
	// count other than 1; a host stack keeps the count it runs instead.
	ReplicasIgnored []string `json:"replicas_ignored,omitempty"`
}

type ServiceResult struct {
	Name        string `json:"name"`
	ContainerID string `json:"container_id"`
	Image       string `json:"image"`
}

type ResourceResult struct {
	Name    string `json:"name"`
	Created bool   `json:"created"`
}

type StatusEntry struct {
	Service     string `json:"service"`
	ContainerID string `json:"container_id"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Image       string `json:"image"`
}

// Deploy is the filesystem entry point: looks the stack up via the stacks
// manager, loads compose.yaml + .env, then hands the parsed project to
// DeployProject which is the actual executor.
func (s *Service) Deploy(ctx context.Context, stackName string) (*DeployResult, error) {
	if s.docker == nil {
		return nil, errors.New("docker unavailable")
	}
	if s.stacks == nil {
		return nil, errors.New("stacks manager not configured")
	}
	dir, err := s.stacks.Dir(stackName)
	if err != nil {
		return nil, err
	}
	// Fetch the decrypted env through the stack manager — this is the
	// only place plaintext secrets exist, and it stays in memory.
	detail, err := s.stacks.Get(stackName)
	if err != nil {
		return nil, err
	}
	proj, err := LoadProject(ctx, dir, stackName, detail.Env)
	if err != nil {
		return nil, err
	}
	return s.DeployProject(ctx, proj)
}

// DeployProject runs the executor against an already-parsed compose project.
// This is the seam that lets the agent reuse the exact same deploy code:
// the agent receives compose+env over the WS, parses it from a tmpdir, and
// calls DeployProject directly. No stacks.Manager dependency needed.
func (s *Service) DeployProject(ctx context.Context, proj *composetypes.Project) (res *DeployResult, deployErr error) {
	if s.docker == nil {
		return nil, errors.New("docker unavailable")
	}
	ctx, span := composeTracer.Start(ctx, "compose.deploy",
		trace.WithAttributes(
			attribute.String("stack", proj.Name),
			attribute.Int("service.count", len(proj.Services)),
			attribute.Int("network.count", len(proj.Networks)),
			attribute.Int("volume.count", len(proj.Volumes)),
		))
	defer span.End()

	cli := s.docker.Raw()
	result := &DeployResult{Stack: proj.Name, ReplicasIgnored: ignoredReplicas(proj)}

	// Live progress for the UI — Begin once, Finish after the loop or
	// on early-exit error. The tracker is nil-safe.
	totalServices := len(proj.Services)
	if s.tracker != nil {
		s.tracker.Begin(proj.Name, totalServices)
		defer func() {
			// Finish records the terminal state. The deferred close
			// catches early returns from the network/volume reconcile
			// paths too; the actual err is shadowed via deployErr.
			s.tracker.Finish(proj.Name, deployErr)
		}()
	}

	netNames, err := s.reconcileNetworks(ctx, cli, proj, result)
	if err != nil {
		span.SetStatus(codes.Error, "reconcile networks")
		span.RecordError(err)
		return nil, err
	}
	if err := s.reconcileVolumes(ctx, cli, proj, result); err != nil {
		span.SetStatus(codes.Error, "reconcile volumes")
		span.RecordError(err)
		return nil, err
	}

	// Deterministic order — we do not yet resolve depends_on.
	names := make([]string, 0, len(proj.Services))
	for n := range proj.Services {
		names = append(names, n)
	}
	sort.Strings(names)

	// Phase 1 — pull every image BEFORE we touch any existing container.
	// If a required image is missing locally AND the pull fails (GHCR
	// down, bad credentials, image not found, …), abort here so the
	// previous deployment keeps running. Without this guard the loop
	// below would stop the old container, then fail to pull, leaving a
	// phantom stack with 502s in the proxy.
	if err := s.prePullImages(ctx, cli, proj, names); err != nil {
		span.SetStatus(codes.Error, "pre-pull")
		span.RecordError(err)
		deployErr = err
		return nil, deployErr
	}

	// Only after the pull: a failed pull must leave the old containers
	// and their data untouched.
	copies, err := s.adoptStagedBindData(ctx, cli, proj)
	result.BindCopies = copies
	if err != nil {
		deployErr = err
		return result, deployErr
	}

	// Phase 2 — tear down + recreate per service. Pulls already happened
	// in phase 1, so deployService never touches the registry.
	for i, name := range names {
		svc := proj.Services[name]
		sr, err := s.deployService(ctx, cli, proj, svc, netNames, i+1)
		if err != nil {
			span.SetStatus(codes.Error, "service "+name)
			span.RecordError(err)
			deployErr = fmt.Errorf("service %s: %w", name, err)
			return nil, deployErr
		}
		result.Services = append(result.Services, *sr)
	}
	res = result
	return result, nil
}

func ignoredReplicas(proj *composetypes.Project) []string {
	var out []string
	for name, svc := range proj.Services {
		if svc.Deploy != nil && svc.Deploy.Replicas != nil && *svc.Deploy.Replicas != 1 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// prePullImages walks every service in deploy order and pulls the image
// when needed (svc.PullPolicy=always, WithForcePull(ctx), or no cached
// copy). A pull failure is fatal ONLY when the image isn't cached
// locally — otherwise we log and keep the cached copy. Runs before any
// container is stopped, so an abort here leaves the existing deployment
// intact.
func (s *Service) prePullImages(ctx context.Context, cli *client.Client, proj *composetypes.Project, names []string) error {
	for i, name := range names {
		svc := proj.Services[name]
		if svc.Image == "" {
			// validated again per-service in deployService — let that
			// error message stand. No pull to attempt either way.
			continue
		}
		_, localErr := cli.ImageInspect(ctx, svc.Image)
		localMissing := cerrdefs.IsNotFound(localErr)
		if localErr != nil && !localMissing {
			return fmt.Errorf("image inspect %s: %w", svc.Image, localErr)
		}
		needsPull := localMissing || ForcePullRequested(ctx) ||
			strings.EqualFold(svc.PullPolicy, "always")
		if !needsPull {
			continue
		}
		if s.tracker != nil {
			s.tracker.SetPhase(proj.Name, progressPulling, svc.Name, svc.Image, i+1)
		}
		pullCtx, pullSpan := composeTracer.Start(ctx, "compose.pull_image",
			trace.WithAttributes(
				attribute.String("stack", proj.Name),
				attribute.String("service", svc.Name),
				attribute.String("image", svc.Image),
				attribute.Bool("forced", !localMissing),
			))
		auth := s.resolveImageAuth(pullCtx, svc.Image)
		rc, err := cli.ImagePull(pullCtx, svc.Image, client.ImagePullOptions{RegistryAuth: auth})
		if err != nil {
			pullSpan.SetStatus(codes.Error, err.Error())
			pullSpan.End()
			if !localMissing {
				slog.Warn("image pull failed, using cached copy",
					"image", svc.Image, "err", err)
				continue
			}
			return fmt.Errorf("image pull %s: %w", svc.Image, err)
		}
		err = docker.ReadPullStream(rc, operations.RegistryAuthSecrets(auth))
		rc.Close()
		if err != nil {
			pullSpan.SetStatus(codes.Error, err.Error())
			pullSpan.End()
			if !localMissing {
				slog.Warn("image pull failed, using cached copy",
					"image", svc.Image, "err", err)
				continue
			}
			return fmt.Errorf("image pull %s: %w", svc.Image, err)
		}
		pullSpan.End()
	}
	return nil
}

func (s *Service) Stop(ctx context.Context, stackName string) error {
	if s.docker == nil {
		return errors.New("docker unavailable")
	}
	cli := s.docker.Raw()
	list, err := listProjectContainers(ctx, cli, stackName)
	if err != nil {
		return err
	}
	for _, c := range list {
		_, _ = cli.ContainerStop(ctx, c.ID, client.ContainerStopOptions{})
		if _, err := cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("remove %s: %w", c.ID[:12], err)
		}
	}
	return nil
}

func (s *Service) Status(ctx context.Context, stackName string) ([]StatusEntry, error) {
	if s.docker == nil {
		return nil, errors.New("docker unavailable")
	}
	list, err := listProjectContainers(ctx, s.docker.Raw(), stackName)
	if err != nil {
		return nil, err
	}
	out := make([]StatusEntry, 0, len(list))
	for _, c := range list {
		out = append(out, StatusEntry{
			Service:     c.Labels[LabelService],
			ContainerID: c.ID,
			State:       string(c.State),
			Status:      c.Status,
			Image:       c.Image,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out, nil
}

// -----------------------------------------------------------------------------
// reconcile helpers
// -----------------------------------------------------------------------------

// reconcileNetworks creates project-scoped networks that don't exist yet and
// returns a map of compose-network-key → actual docker network name.
func (s *Service) reconcileNetworks(ctx context.Context, cli *client.Client, proj *composetypes.Project, res *DeployResult) (map[string]string, error) {
	out := make(map[string]string, len(proj.Networks))
	for key, net := range proj.Networks {
		actualName := net.Name
		if actualName == "" {
			actualName = proj.Name + "_" + key
		}
		out[key] = actualName

		if bool(net.External) {
			continue
		}
		if _, err := cli.NetworkInspect(ctx, actualName, client.NetworkInspectOptions{}); err == nil {
			res.Networks = append(res.Networks, ResourceResult{Name: actualName, Created: false})
			continue
		} else if !cerrdefs.IsNotFound(err) {
			return nil, fmt.Errorf("network inspect %s: %w", actualName, err)
		}

		labels := map[string]string{
			LabelProject: proj.Name,
			LabelManaged: "true",
		}
		for k, v := range net.Labels {
			labels[k] = v
		}
		driver := net.Driver
		if driver == "" {
			driver = "bridge"
		}
		if _, err := cli.NetworkCreate(ctx, actualName, client.NetworkCreateOptions{
			Driver:     driver,
			Internal:   net.Internal,
			Attachable: net.Attachable,
			Labels:     labels,
			Options:    net.DriverOpts,
		}); err != nil {
			return nil, fmt.Errorf("network create %s: %w", actualName, err)
		}
		res.Networks = append(res.Networks, ResourceResult{Name: actualName, Created: true})
	}
	return out, nil
}

func (s *Service) reconcileVolumes(ctx context.Context, cli *client.Client, proj *composetypes.Project, res *DeployResult) error {
	for key, vol := range proj.Volumes {
		actualName := vol.Name
		if actualName == "" {
			actualName = proj.Name + "_" + key
		}
		if bool(vol.External) {
			continue
		}
		if _, err := cli.VolumeInspect(ctx, actualName, client.VolumeInspectOptions{}); err == nil {
			res.Volumes = append(res.Volumes, ResourceResult{Name: actualName, Created: false})
			continue
		} else if !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("volume inspect %s: %w", actualName, err)
		}

		labels := map[string]string{
			LabelProject: proj.Name,
			LabelManaged: "true",
		}
		for k, v := range vol.Labels {
			labels[k] = v
		}
		driver := vol.Driver
		if driver == "" {
			driver = "local"
		}
		if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{
			Name:       actualName,
			Driver:     driver,
			DriverOpts: vol.DriverOpts,
			Labels:     labels,
		}); err != nil {
			return fmt.Errorf("volume create %s: %w", actualName, err)
		}
		res.Volumes = append(res.Volumes, ResourceResult{Name: actualName, Created: true})
	}
	return nil
}

// -----------------------------------------------------------------------------
// per-service deployment
// -----------------------------------------------------------------------------

func (s *Service) deployService(ctx context.Context, cli *client.Client, proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string, step int) (*ServiceResult, error) {
	if svc.Image == "" {
		return nil, errors.New("image is required (build: not supported in phase 1)")
	}
	track := func(phase string) {
		if s.tracker != nil {
			s.tracker.SetPhase(proj.Name, phase, svc.Name, svc.Image, step)
		}
	}

	replicas, err := replicaPlan(ctx, cli, proj, svc)
	if err != nil {
		return nil, err
	}
	var firstID string
	for i, rp := range replicas {
		id, err := s.replaceReplica(ctx, cli, proj, svc, netNames, rp, i+1, track)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			firstID = id
		}
	}
	return &ServiceResult{Name: svc.Name, ContainerID: firstID, Image: svc.Image}, nil
}

// plannedReplica is one container a deploy creates, with the containers
// it replaces.
type plannedReplica struct {
	name string
	old  []container.Summary
}

// replicaPlan keeps the number of replicas a service has, so a deploy
// renews every replica with the new config. A service with
// container_name or without containers gets one.
func replicaPlan(ctx context.Context, cli *client.Client, proj *composetypes.Project, svc composetypes.ServiceConfig) ([]plannedReplica, error) {
	if svc.ContainerName != "" {
		return []plannedReplica{{name: svc.ContainerName}}, nil
	}
	existing, err := listServiceContainers(ctx, cli, proj.Name, svc.Name)
	if err != nil {
		return nil, err
	}
	sortByReplicaIndex(existing, proj.Name, svc.Name)
	n := min(max(len(existing), 1), maxReplicas)
	plan := make([]plannedReplica, n)
	for i := range plan {
		plan[i].name = replicaName(proj.Name, svc.Name, i+1)
		if i < len(existing) {
			plan[i].old = []container.Summary{existing[i]}
		}
	}
	return plan, nil
}

// maxReplicas is the most replicas a service runs on a host.
const maxReplicas = 100

// ownedBy refuses a container that compose labels as part of another
// stack or service: replica names of different stacks can collide
// ("a" + "b-c" and "a-b" + "c" both give "a-b-c-1").
func ownedBy(cfg *container.Config, stack, service string) error {
	if cfg == nil {
		return nil
	}
	p, ok := cfg.Labels[LabelProject]
	if !ok {
		return nil
	}
	if p != stack || cfg.Labels[LabelService] != service {
		return fmt.Errorf("name is taken by service %s of stack %s", cfg.Labels[LabelService], p)
	}
	return nil
}

// removeContainer stops and removes a container; one already gone is fine.
func removeContainer(ctx context.Context, cli *client.Client, id string) error {
	_, _ = cli.ContainerStop(ctx, id, client.ContainerStopOptions{})
	if _, err := cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func replicaName(stack, service string, index int) string {
	return fmt.Sprintf("%s-%s-%d", stack, service, index)
}

// replaceReplica removes the containers rp replaces and any container
// holding its name, then creates and starts the replica and waits until
// it is healthy, so the other replicas keep serving meanwhile.
func (s *Service) replaceReplica(ctx context.Context, cli *client.Client, proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string, rp plannedReplica, index int, track func(string)) (string, error) {
	for _, c := range rp.old {
		if err := removeContainer(ctx, cli, c.ID); err != nil {
			return "", fmt.Errorf("remove existing %s: %w", rp.name, err)
		}
	}
	if existing, err := cli.ContainerInspect(ctx, rp.name, client.ContainerInspectOptions{}); err == nil {
		if err := ownedBy(existing.Container.Config, proj.Name, svc.Name); err != nil {
			return "", fmt.Errorf("%s: %w", rp.name, err)
		}
		if err := removeContainer(ctx, cli, existing.Container.ID); err != nil {
			return "", fmt.Errorf("remove existing %s: %w", rp.name, err)
		}
	}

	cfg, hostCfg, netCfg, err := replicaContainerConfig(proj, svc, netNames, index)
	if err != nil {
		return "", err
	}

	startCtx, startSpan := composeTracer.Start(ctx, "compose.start_container",
		trace.WithAttributes(
			attribute.String("stack", proj.Name),
			attribute.String("service", svc.Name),
			attribute.String("image", svc.Image),
			attribute.String("container.name", rp.name),
		))
	defer startSpan.End()

	track(progressCreating)
	resp, err := cli.ContainerCreate(startCtx, client.ContainerCreateOptions{Config: cfg, HostConfig: hostCfg, NetworkingConfig: netCfg, Name: rp.name})
	if err != nil {
		startSpan.SetStatus(codes.Error, "create")
		startSpan.RecordError(err)
		return "", fmt.Errorf("container create %s: %w", rp.name, err)
	}
	track(progressStarting)
	startSpan.SetAttributes(attribute.String("container.id", resp.ID))
	if _, err := cli.ContainerStart(startCtx, resp.ID, client.ContainerStartOptions{}); err != nil {
		startSpan.SetStatus(codes.Error, "start")
		startSpan.RecordError(err)
		return "", fmt.Errorf("container start %s: %w", rp.name, err)
	}
	track(progressHealthwait)
	if err := WaitHealthy(startCtx, cli, resp.ID, &svc); err != nil {
		startSpan.SetStatus(codes.Error, "unhealthy")
		startSpan.RecordError(err)
		return "", fmt.Errorf("service %s did not become healthy: %w", svc.Name, err)
	}
	return resp.ID, nil
}

// Replicas after the first get unless-stopped when the service sets no
// restart policy: without it a crashed extra replica stays down until
// the next scale.
func replicaContainerConfig(proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string, index int) (*container.Config, *container.HostConfig, *dnetwork.NetworkingConfig, error) {
	cfg, hostCfg, netCfg, err := serviceToContainerConfig(proj, svc, netNames)
	if err != nil {
		return nil, nil, nil, err
	}
	if index > 1 && (hostCfg.RestartPolicy.Name == "" || hostCfg.RestartPolicy.Name == "no") {
		hostCfg.RestartPolicy.Name = "unless-stopped"
	}
	return cfg, hostCfg, netCfg, nil
}

func serviceToContainerConfig(proj *composetypes.Project, svc composetypes.ServiceConfig, netNames map[string]string) (*container.Config, *container.HostConfig, *dnetwork.NetworkingConfig, error) {
	// Environment: compose uses MappingWithEquals (map[string]*string); nil means "inherit".
	env := make([]string, 0, len(svc.Environment))
	for k, v := range svc.Environment {
		if v == nil {
			continue
		}
		env = append(env, k+"="+*v)
	}
	sort.Strings(env)

	// Ports.
	exposed := dnetwork.PortSet{}
	bindings := dnetwork.PortMap{}
	for _, p := range svc.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		port, err := dnetwork.ParsePort(fmt.Sprintf("%d/%s", p.Target, proto))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("port %d/%s: %w", p.Target, proto, err)
		}
		var hostIP netip.Addr
		if p.HostIP != "" {
			if hostIP, err = netip.ParseAddr(p.HostIP); err != nil {
				return nil, nil, nil, fmt.Errorf("port %d/%s: host ip: %w", p.Target, proto, err)
			}
		}
		exposed[port] = struct{}{}
		bindings[port] = append(bindings[port], dnetwork.PortBinding{
			HostIP:   hostIP,
			HostPort: p.Published,
		})
	}

	// Volumes → split into bind strings and named-volume mounts.
	var binds []string
	var mounts []mount.Mount
	for _, v := range svc.Volumes {
		switch v.Type {
		case composetypes.VolumeTypeBind, "":
			// Default short form is already bind. compose-go sets Type="".
			if v.Source == "" {
				continue
			}
			bind := fmt.Sprintf("%s:%s", v.Source, v.Target)
			if v.ReadOnly {
				bind += ":ro"
			}
			binds = append(binds, bind)
		case composetypes.VolumeTypeVolume:
			source := v.Source
			if vol, ok := proj.Volumes[v.Source]; ok {
				if vol.Name != "" {
					source = vol.Name
				} else {
					source = proj.Name + "_" + v.Source
				}
			}
			mounts = append(mounts, mount.Mount{
				Type:     mount.TypeVolume,
				Source:   source,
				Target:   v.Target,
				ReadOnly: v.ReadOnly,
			})
		case composetypes.VolumeTypeTmpfs:
			mounts = append(mounts, mount.Mount{Type: mount.TypeTmpfs, Target: v.Target})
		default:
			return nil, nil, nil, fmt.Errorf("unsupported volume type %q", v.Type)
		}
	}

	// Labels — merge user labels with our tracking labels.
	labels := map[string]string{
		LabelProject: proj.Name,
		LabelService: svc.Name,
		LabelManaged: "true",
	}
	for k, v := range svc.Labels {
		labels[k] = v
	}

	cfg := &container.Config{
		Image:        svc.Image,
		Env:          env,
		Cmd:          []string(svc.Command),
		Entrypoint:   []string(svc.Entrypoint),
		Labels:       labels,
		ExposedPorts: exposed,
		User:         svc.User,
		WorkingDir:   svc.WorkingDir,
		Hostname:     svc.Hostname,
		Tty:          svc.Tty,
		OpenStdin:    svc.StdinOpen,
		Healthcheck:  composeHealthToDocker(svc.HealthCheck),
	}

	hostCfg := &container.HostConfig{
		PortBindings:   bindings,
		Binds:          binds,
		Mounts:         mounts,
		Privileged:     svc.Privileged,
		ReadonlyRootfs: svc.ReadOnly,
		CapAdd:         svc.CapAdd,
		CapDrop:        svc.CapDrop,
	}
	if svc.Restart != "" {
		hostCfg.RestartPolicy = container.RestartPolicy{
			Name: container.RestartPolicyMode(svc.Restart),
		}
	}
	if svc.NetworkMode != "" {
		hostCfg.NetworkMode = container.NetworkMode(svc.NetworkMode)
	}

	netCfg := &dnetwork.NetworkingConfig{}
	if len(svc.Networks) > 0 {
		netCfg.EndpointsConfig = make(map[string]*dnetwork.EndpointSettings, len(svc.Networks))
		// Deterministic — pick lowest-priority-sort network for the initial
		// attach; additional networks would need a post-create connect which
		// we'll add later.
		keys := svc.NetworksByPriority()
		for _, k := range keys {
			actual, ok := netNames[k]
			if !ok {
				actual = proj.Name + "_" + k
			}
			endpoint := &dnetwork.EndpointSettings{}
			// Mirror Docker Compose CLI default: every replica of a service
			// shares the service name as a network alias so Docker's embedded
			// DNS round-robins across them. Without this, scaling `app` to 3
			// replicas gives three containers that are only reachable by
			// their full `<stack>-<service>-<N>` name, defeating load
			// distribution for in-network callers.
			aliases := []string{svc.Name}
			if nc := svc.Networks[k]; nc != nil {
				for _, a := range nc.Aliases {
					if a != svc.Name {
						aliases = append(aliases, a)
					}
				}
			}
			endpoint.Aliases = aliases
			netCfg.EndpointsConfig[actual] = endpoint
		}
	}

	return cfg, hostCfg, netCfg, nil
}

func listProjectContainers(ctx context.Context, cli *client.Client, stackName string) ([]container.Summary, error) {
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", LabelProject+"="+stackName)})
	if err != nil {
		return nil, err
	}
	// Sanity: filter again locally in case the daemon returns something unexpected.
	out := list.Items[:0]
	for _, c := range list.Items {
		if strings.EqualFold(c.Labels[LabelProject], stackName) {
			out = append(out, c)
		}
	}
	return out, nil
}
