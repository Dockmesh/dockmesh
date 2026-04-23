// Package host abstracts a single docker daemon — local or remote via
// agent — behind one interface so HTTP handlers can talk to either with
// the same call. Concept §3.1.
//
// Slice 3.1.2 ships read-only container/image/network/volume operations
// over the agent. Mutations (start/stop/remove) and streaming (logs, exec,
// stats) come in 3.1.2.1 / 3.1.2.2.
package host

import (
	"context"
	"io"

	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/system"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"
)

// Host is what HTTP handlers call. Both LocalHost (wrapping the embedded
// docker.Client) and RemoteHost (proxying via an agent's WS) implement it.
type Host interface {
	// Identity
	ID() string
	Name() string

	// Container reads
	ListContainers(ctx context.Context, all bool) ([]dtypes.Container, error)
	InspectContainer(ctx context.Context, id string) (dtypes.ContainerJSON, error)

	// Container mutations (slice 3.1.2.1)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string) error
	RestartContainer(ctx context.Context, id string) error
	RemoveContainer(ctx context.Context, id string, force bool) error

	// Pause / unpause freeze and thaw all processes in a container via
	// the freezer cgroup. No data lost, useful for incident-response
	// inspection. KillContainer sends a signal (default SIGKILL if
	// signal == ""). P.11.4.
	PauseContainer(ctx context.Context, id string) error
	UnpauseContainer(ctx context.Context, id string) error
	KillContainer(ctx context.Context, id, signal string) error

	// Container log stream (slice 3.1.2.2). The returned ReadCloser
	// produces docker's multiplexed log frame format (8-byte mux header
	// per chunk for non-tty containers) — the WS handler scans line-by-
	// line and strips the header. Both LocalHost and RemoteHost expose
	// the same wire format so handler code doesn't branch.
	ContainerLogs(ctx context.Context, id string, tail string, follow bool) (io.ReadCloser, error)

	// Container stats stream (slice 3.1.2.3). Newline-delimited JSON
	// matching the docker /containers/{id}/stats?stream=true response.
	ContainerStats(ctx context.Context, id string) (io.ReadCloser, error)

	// Interactive exec session (slice 3.1.2.4). Returns an ExecSession
	// that owns stdin/stdout via Read/Write and a Resize op for tty
	// dimensions. Close terminates the session.
	StartExec(ctx context.Context, id string, cmd []string) (ExecSession, error)

	// Resource lists + mutations
	ListImages(ctx context.Context, all bool) ([]dtypes.ImageSummary, error)
	RemoveImage(ctx context.Context, id string, force bool) ([]dtypes.ImageDeleteResponseItem, error)
	PruneImages(ctx context.Context) (dtypes.ImagesPruneReport, error)
	ListNetworks(ctx context.Context) ([]dtypes.NetworkResource, error)
	InspectNetwork(ctx context.Context, id string) (dtypes.NetworkResource, error)
	ListVolumes(ctx context.Context) ([]any, error)
	InspectVolume(ctx context.Context, name string) (volume.Volume, error)

	// VolumeBrowseEntries lists one directory level inside a docker
	// volume. subpath is relative to the volume root; "" or "/" means
	// the volume root itself. Rejects path-traversal attempts (P.11.8).
	VolumeBrowseEntries(ctx context.Context, name, subpath string) ([]VolumeEntry, error)
	// VolumeReadFile reads a single regular file inside the volume,
	// capped at maxBytes. Returns truncated=true when the file is
	// larger than the cap so the UI can offer a download link
	// (stream support is a follow-up).
	VolumeReadFile(ctx context.Context, name, subpath string, maxBytes int64) (*VolumeFileResult, error)

	// VolumeTar opens a tar.gz stream of the whole volume (used by
	// backup jobs that target this host). Caller Close()s when done.
	// Local hosts spawn a helper container against the docker socket;
	// remote hosts run the same helper on the agent side and relay
	// bytes over the existing stream framing.
	VolumeTar(ctx context.Context, name string) (io.ReadCloser, error)

	// ContainerExec runs `cmd` inside `container` and blocks until the
	// command exits. Stdout/stderr go into the returned buffer (capped).
	// Used by backup pre-hooks (e.g. pg_dump) so operators can quiesce
	// a database before the tar. Works on both local and remote hosts.
	ContainerExec(ctx context.Context, containerID string, cmd []string) (stdout []byte, exitCode int, err error)

	// Stack operations (slice 3.1.3). The handler reads the compose+env
	// from the central server's filesystem once and passes the content
	// to whichever host — local writes to a tmpdir + parses + runs;
	// remote ships the payload over the agent WS.
	DeployStack(ctx context.Context, name, composeYAML, envContent string) (*compose.DeployResult, error)
	StopStack(ctx context.Context, name string) error
	StackStatus(ctx context.Context, name string) ([]compose.StatusEntry, error)

	// Cleanup removes project-scoped networks / volumes / images for a
	// stack. Opt-in per-category; containers must already be stopped.
	// CleanupPreview lists what Cleanup would remove so the UI can
	// render a confirmation dialog without actually mutating anything.
	CleanupStack(ctx context.Context, name string, opts compose.CleanupOpts) (*compose.CleanupResult, error)
	CleanupPreview(ctx context.Context, name string) (*compose.CleanupPlan, error)

	// Scale a single service within a stack to the desired replica
	// count (P.8). compose+env content is shipped alongside so both
	// local and remote can parse the project. Safety validation
	// (container_name, hard ports) is done in the compose package.
	ScaleService(ctx context.Context, name, composeYAML, envContent, service string, replicas int) (*compose.ScaleResult, error)
	CheckScale(ctx context.Context, name, composeYAML, envContent, service string) (*compose.ScaleCheck, error)

	// Rolling replace of a single service's replicas (P.12.5b). Remote
	// hosts return an explicit NotImplemented-style error until the
	// agent protocol gains the matching frame type.
	RollingReplace(ctx context.Context, name, composeYAML, envContent, service string, opts compose.RollingOptions) (*compose.RollingResult, error)

	// Host-level system metrics (CPU / memory / disk / uptime). Locally
	// this reads /proc and statfs; remotely it asks the agent to do the
	// same and ships the result back. Used by the dashboard's all-mode
	// System Health panel which renders one row per host. Slice P.6.
	SystemMetrics(ctx context.Context) (system.Metrics, error)
}

// ExecSession is the interface the WS exec handler uses to talk to a
// running interactive exec instance. LocalExecSession wraps a docker
// HijackedResponse; RemoteExecSession wraps a multiplexed agent stream.
type ExecSession interface {
	io.Reader // stdout/stderr (tty mode merges them)
	io.Writer // stdin
	Resize(rows, cols uint) error
	Close() error
}

// Info is what /api/v1/hosts returns for the frontend host switcher.
type Info struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`           // "local" | "agent"
	Status string   `json:"status"`         // "online" | "offline"
	Tags   []string `json:"tags,omitempty"` // populated by the handler from hosttags service
}
