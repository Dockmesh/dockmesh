//go:build composelib

// Package composelib drives stacks through the docker/compose SDK
// (github.com/docker/compose/v5/pkg/compose) instead of the hand-written
// reconciler in internal/compose. It is only built with -tags composelib.
package composelib

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/config/configfile"
	clitypes "github.com/docker/cli/cli/config/types"
	"github.com/docker/cli/cli/context/docker"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/client"
)

// Event is one progress update from the SDK, flattened for the deploy
// tracker / WebSocket stream. ID is "Container <name>", "Image <ref>",
// "Network <name>", "Volume <name>" or api.ResourceCompose.
type Event struct {
	Operation string
	ID        string
	ParentID  string
	Text      string
	Details   string
	Status    string
	Percent   int
}

// Engine owns one moby API client per daemon. A compose service is built
// per call because registry credentials and the event sink are per deploy.
type Engine struct {
	api *client.Client
}

// New connects to host (empty = DOCKER_HOST / default socket).
func New(host string) (*Engine, error) {
	opts := []client.Opt{client.FromEnv}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	c, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Engine{api: c}, nil
}

func (e *Engine) Close() error { return e.api.Close() }

// Options configures a single SDK call.
type Options struct {
	// RegistryAuth maps a registry host ("ghcr.io", "https://index.docker.io/v1/")
	// to base64 X-Registry-Auth blobs as produced by registries.Service.
	RegistryAuth map[string]string
	OnEvent      func(Event)
	// Log receives the SDK's plain-text output (warnings such as orphan
	// containers). Defaults to io.Discard.
	Log io.Writer
}

func (e *Engine) service(opts Options) (api.Compose, error) {
	cfg, err := configFromAuth(opts.RegistryAuth)
	if err != nil {
		return nil, err
	}
	base, err := command.NewDockerCli(
		command.WithAPIClient(e.api),
		command.WithCombinedStreams(logWriter(opts.Log)),
	)
	if err != nil {
		return nil, err
	}
	cli := &dmCli{DockerCli: base, api: e.api, cfg: cfg}
	return compose.NewComposeService(cli,
		compose.WithEventProcessor(&eventSink{fn: opts.OnEvent}),
		compose.WithPrompt(compose.AlwaysOkPrompt()),
		compose.WithStreams(logWriter(opts.Log), logWriter(opts.Log), emptyReader{}),
		compose.WithContextInfo(contextInfo{}),
		compose.WithProxyConfig(map[string]string{}),
	)
}

// UpOptions maps Dockmesh deploy semantics onto api.UpOptions.
type UpOptions struct {
	ForcePull      bool
	ForceRecreate  bool
	RemoveOrphans  bool
	WaitHealthy    bool
	HealthyTimeout time.Duration
}

// Up converges the project: image pull (missing or forced) happens before
// any container is touched, unchanged services are left running, and with
// WaitHealthy it only returns once every service is running/healthy.
func (e *Engine) Up(ctx context.Context, proj *types.Project, uo UpOptions, opts Options) error {
	svc, err := e.service(opts)
	if err != nil {
		return err
	}
	if uo.ForcePull {
		for name, s := range proj.Services {
			if s.Build == nil {
				s.PullPolicy = types.PullPolicyAlways
				proj.Services[name] = s
			}
		}
	}
	recreate := api.RecreateDiverged
	if uo.ForceRecreate {
		recreate = api.RecreateForce
	}
	return svc.Up(ctx, proj, api.UpOptions{
		Create: api.CreateOptions{
			RemoveOrphans:        uo.RemoveOrphans,
			Recreate:             recreate,
			RecreateDependencies: api.RecreateDiverged,
			Inherit:              true,
		},
		Start: api.StartOptions{
			Project:     proj,
			Wait:        uo.WaitHealthy,
			WaitTimeout: uo.HealthyTimeout,
		},
	})
}

// Pull pulls every image without touching containers.
func (e *Engine) Pull(ctx context.Context, proj *types.Project, opts Options) error {
	svc, err := e.service(opts)
	if err != nil {
		return err
	}
	return svc.Pull(ctx, proj, api.PullOptions{IgnoreBuildable: true})
}

// Down removes the project's containers and networks; volumes are kept.
func (e *Engine) Down(ctx context.Context, project string, removeOrphans bool, opts Options) error {
	svc, err := e.service(opts)
	if err != nil {
		return err
	}
	return svc.Down(ctx, project, api.DownOptions{RemoveOrphans: removeOrphans})
}

// Ps lists the project's containers, stopped ones included.
func (e *Engine) Ps(ctx context.Context, project string) ([]api.ContainerSummary, error) {
	svc, err := e.service(Options{})
	if err != nil {
		return nil, err
	}
	return svc.Ps(ctx, project, api.PsOptions{All: true})
}

// Scale sets replica counts and converges only those services.
func (e *Engine) Scale(ctx context.Context, proj *types.Project, replicas map[string]int, opts Options) error {
	svc, err := e.service(opts)
	if err != nil {
		return err
	}
	return svc.Scale(ctx, proj, api.ScaleOptions{Replicas: replicas})
}

// ConfigHash is the value compose stores in com.docker.compose.config-hash;
// comparing it with the running container's label tells whether a deploy
// would recreate the service.
func ConfigHash(s types.ServiceConfig) (string, error) { return compose.ServiceHash(s) }

// configFromAuth builds an in-memory CLI config so the SDK never reads
// ~/.docker/config.json or a credential helper of the user running Dockmesh.
func configFromAuth(blobs map[string]string) (*configfile.ConfigFile, error) {
	cfg := configfile.New("")
	for host, blob := range blobs {
		raw, err := base64.URLEncoding.DecodeString(blob)
		if err != nil {
			return nil, fmt.Errorf("registry auth for %s: %w", host, err)
		}
		var ac clitypes.AuthConfig
		if err := json.Unmarshal(raw, &ac); err != nil {
			return nil, fmt.Errorf("registry auth for %s: %w", host, err)
		}
		ac.ServerAddress = host
		cfg.AuthConfigs[host] = ac
	}
	return cfg, nil
}

// dmCli pins every command.Cli method the SDK reaches that would otherwise
// consult the docker context store or config dir. DockerCli.Client() calls
// os.Exit(1) when it cannot resolve an endpoint, so it must never run.
type dmCli struct {
	*command.DockerCli
	api *client.Client
	cfg *configfile.ConfigFile
}

func (c *dmCli) Client() client.APIClient            { return c.api }
func (c *dmCli) ConfigFile() *configfile.ConfigFile  { return c.cfg }
func (c *dmCli) CurrentContext() string              { return "default" }
func (c *dmCli) DockerEndpoint() docker.Endpoint     { return docker.Endpoint{} }
func (c *dmCli) BuildKitEnabled() (bool, error)      { return false, nil }
func (c *dmCli) ServerInfo() command.ServerInfo      { return command.ServerInfo{OSType: "linux"} }

// contextInfo keeps builds on the API-only classic builder: with BuildKit
// enabled and buildx installed the SDK shells out to `docker buildx bake`.
type contextInfo struct{}

func (contextInfo) CurrentContext() string         { return "default" }
func (contextInfo) ServerOSType() string           { return "linux" }
func (contextInfo) BuildKitEnabled() (bool, error) { return false, nil }

type eventSink struct {
	fn func(Event)
	op string
}

func (s *eventSink) Start(_ context.Context, operation string) { s.op = operation }
func (s *eventSink) Done(string, bool)                         {}

func (s *eventSink) On(events ...api.Resource) {
	if s.fn == nil {
		return
	}
	for _, r := range events {
		s.fn(Event{
			Operation: s.op,
			ID:        r.ID,
			ParentID:  r.ParentID,
			Text:      r.Text,
			Details:   r.Details,
			Status:    r.StatusText(),
			Percent:   r.Percent,
		})
	}
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

func logWriter(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// IsNotFound reports whether err means the project has no containers.
func IsNotFound(err error) bool { return errors.Is(err, api.ErrNotFound) }
