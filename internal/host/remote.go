package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/system"
	dtypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/volume"
)

// RemoteHost proxies operations to a connected agent over its WebSocket
// using the request/response protocol in internal/agents.
type RemoteHost struct {
	id    string
	name  string
	agent *agents.ConnectedAgent
}

func NewRemote(id, name string, ag *agents.ConnectedAgent) *RemoteHost {
	return &RemoteHost{id: id, name: name, agent: ag}
}

func (h *RemoteHost) ID() string   { return h.id }
func (h *RemoteHost) Name() string { return h.name }

func (h *RemoteHost) request(ctx context.Context, frameType string, payload any) (json.RawMessage, error) {
	if h.agent == nil {
		return nil, ErrAgentOffline
	}
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	env, err := h.agent.Request(ctx, agents.Frame{Type: frameType, Payload: raw})
	if err != nil {
		return nil, err
	}
	return env.Data, nil
}

func (h *RemoteHost) ListContainers(ctx context.Context, all bool) ([]dtypes.Container, error) {
	data, err := h.request(ctx, agents.FrameReqContainerList, agents.ContainerListReq{All: all})
	if err != nil {
		return nil, err
	}
	var out []dtypes.Container
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	if out == nil {
		out = []dtypes.Container{}
	}
	return out, nil
}

func (h *RemoteHost) InspectContainer(ctx context.Context, id string) (dtypes.ContainerJSON, error) {
	data, err := h.request(ctx, agents.FrameReqContainerInspect, agents.ContainerIDReq{ID: id})
	if err != nil {
		return dtypes.ContainerJSON{}, err
	}
	var out dtypes.ContainerJSON
	if err := json.Unmarshal(data, &out); err != nil {
		return dtypes.ContainerJSON{}, fmt.Errorf("decode inspect: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) StartContainer(ctx context.Context, id string) error {
	_, err := h.request(ctx, agents.FrameReqContainerStart, agents.ContainerIDReq{ID: id})
	return err
}

func (h *RemoteHost) StopContainer(ctx context.Context, id string) error {
	_, err := h.request(ctx, agents.FrameReqContainerStop, agents.ContainerIDReq{ID: id})
	return err
}

func (h *RemoteHost) RestartContainer(ctx context.Context, id string) error {
	_, err := h.request(ctx, agents.FrameReqContainerRestart, agents.ContainerIDReq{ID: id})
	return err
}

func (h *RemoteHost) RemoveContainer(ctx context.Context, id string, force bool) error {
	_, err := h.request(ctx, agents.FrameReqContainerRemove, agents.ContainerIDReq{ID: id, Force: force})
	return err
}

func (h *RemoteHost) PauseContainer(ctx context.Context, id string) error {
	_, err := h.request(ctx, agents.FrameReqContainerPause, agents.ContainerIDReq{ID: id})
	return err
}

func (h *RemoteHost) UnpauseContainer(ctx context.Context, id string) error {
	_, err := h.request(ctx, agents.FrameReqContainerUnpause, agents.ContainerIDReq{ID: id})
	return err
}

func (h *RemoteHost) KillContainer(ctx context.Context, id, signal string) error {
	_, err := h.request(ctx, agents.FrameReqContainerKill, agents.ContainerKillReq{ID: id, Signal: signal})
	return err
}

func (h *RemoteHost) ContainerLogs(ctx context.Context, id, tail string, follow bool) (io.ReadCloser, error) {
	if h.agent == nil {
		return nil, ErrAgentOffline
	}
	stream, err := h.agent.OpenStream(ctx, "logs", id, map[string]any{
		"tail":   tail,
		"follow": follow,
	})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (h *RemoteHost) ContainerStats(ctx context.Context, id string) (io.ReadCloser, error) {
	if h.agent == nil {
		return nil, ErrAgentOffline
	}
	return h.agent.OpenStream(ctx, "stats", id, nil)
}

func (h *RemoteHost) StartExec(ctx context.Context, id string, cmd []string) (ExecSession, error) {
	if h.agent == nil {
		return nil, ErrAgentOffline
	}
	stream, err := h.agent.OpenStream(ctx, "exec", id, map[string]any{
		"cmd": cmd,
		// Sensible TTY defaults; the browser sends a resize as soon as it
		// has measured xterm.fit so this rarely shows.
		"cols": 80,
		"rows": 24,
	})
	if err != nil {
		return nil, err
	}
	return &remoteExecSession{stream: stream}, nil
}

// remoteExecSession adapts an agent stream to the ExecSession interface.
// Reads pull from the stream's incoming buffer (stdout). Writes push
// stream.data frames in the server → agent direction (stdin). Resize
// uses an out-of-band stream.control frame.
type remoteExecSession struct {
	stream *agents.Stream
}

func (s *remoteExecSession) Read(p []byte) (int, error)  { return s.stream.Read(p) }
func (s *remoteExecSession) Write(p []byte) (int, error) {
	if err := s.stream.WriteFrame(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (s *remoteExecSession) Resize(rows, cols uint) error {
	return s.stream.WriteControl("resize", map[string]any{"cols": cols, "rows": rows})
}
func (s *remoteExecSession) Close() error { return s.stream.Close() }

// -----------------------------------------------------------------------------
// Stack operations
// -----------------------------------------------------------------------------

func (h *RemoteHost) DeployStack(ctx context.Context, name, composeYAML, envContent string) (*compose.DeployResult, error) {
	data, err := h.request(ctx, agents.FrameReqStackDeploy, agents.StackDeployReq{
		Name:    name,
		Compose: composeYAML,
		Env:     envContent,
	})
	if err != nil {
		return nil, err
	}
	var out compose.DeployResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode deploy result: %w", err)
	}
	return &out, nil
}

func (h *RemoteHost) StopStack(ctx context.Context, name string) error {
	_, err := h.request(ctx, agents.FrameReqStackStop, agents.StackNameReq{Name: name})
	return err
}

func (h *RemoteHost) StackStatus(ctx context.Context, name string) ([]compose.StatusEntry, error) {
	data, err := h.request(ctx, agents.FrameReqStackStatus, agents.StackNameReq{Name: name})
	if err != nil {
		return nil, err
	}
	var out []compose.StatusEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	if out == nil {
		out = []compose.StatusEntry{}
	}
	return out, nil
}

func (h *RemoteHost) ListImages(ctx context.Context, all bool) ([]dtypes.ImageSummary, error) {
	data, err := h.request(ctx, agents.FrameReqImageList, map[string]bool{"all": all})
	if err != nil {
		return nil, err
	}
	var out []dtypes.ImageSummary
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode images: %w", err)
	}
	if out == nil {
		out = []dtypes.ImageSummary{}
	}
	return out, nil
}

func (h *RemoteHost) ListNetworks(ctx context.Context) ([]dtypes.NetworkResource, error) {
	data, err := h.request(ctx, agents.FrameReqNetworkList, nil)
	if err != nil {
		return nil, err
	}
	var out []dtypes.NetworkResource
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode networks: %w", err)
	}
	if out == nil {
		out = []dtypes.NetworkResource{}
	}
	return out, nil
}

func (h *RemoteHost) ListVolumes(ctx context.Context) ([]any, error) {
	data, err := h.request(ctx, agents.FrameReqVolumeList, nil)
	if err != nil {
		return nil, err
	}
	var out []any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode volumes: %w", err)
	}
	if out == nil {
		out = []any{}
	}
	return out, nil
}

// SystemMetrics asks the agent to read its own /proc + statfs and return
// a Metrics snapshot. Empty payload. The agent handler mirrors the local
// path — it calls system.Collect() on its own host and ships the result.
// Used by the dashboard's all-mode System Health panel.
func (h *RemoteHost) SystemMetrics(ctx context.Context) (system.Metrics, error) {
	data, err := h.request(ctx, agents.FrameReqSystemMetrics, nil)
	if err != nil {
		return system.Metrics{}, err
	}
	var out system.Metrics
	if err := json.Unmarshal(data, &out); err != nil {
		return system.Metrics{}, fmt.Errorf("decode system metrics: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) InspectNetwork(ctx context.Context, id string) (dtypes.NetworkResource, error) {
	data, err := h.request(ctx, agents.FrameReqNetworkInspect, agents.ResourceIDReq{ID: id})
	if err != nil {
		return dtypes.NetworkResource{}, err
	}
	var out dtypes.NetworkResource
	if err := json.Unmarshal(data, &out); err != nil {
		return dtypes.NetworkResource{}, fmt.Errorf("decode network inspect: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) InspectVolume(ctx context.Context, name string) (volume.Volume, error) {
	data, err := h.request(ctx, agents.FrameReqVolumeInspect, agents.ResourceIDReq{ID: name})
	if err != nil {
		return volume.Volume{}, err
	}
	var out volume.Volume
	if err := json.Unmarshal(data, &out); err != nil {
		return volume.Volume{}, fmt.Errorf("decode volume inspect: %w", err)
	}
	return out, nil
}

// VolumeBrowseEntries / VolumeReadFile proxy to the agent via the
// browse frames. The agent runs the same BrowseDir / ReadFile helpers
// locally against its own mounted volume fs. P.11.8.
func (h *RemoteHost) VolumeBrowseEntries(ctx context.Context, name, subpath string) ([]VolumeEntry, error) {
	data, err := h.request(ctx, agents.FrameReqVolumeBrowse, agents.VolumeBrowseReq{Volume: name, SubPath: subpath})
	if err != nil {
		return nil, err
	}
	var out []VolumeEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode volume browse: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) VolumeReadFile(ctx context.Context, name, subpath string, maxBytes int64) (*VolumeFileResult, error) {
	data, err := h.request(ctx, agents.FrameReqVolumeBrowseFile,
		agents.VolumeBrowseReq{Volume: name, SubPath: subpath, MaxBytes: maxBytes})
	if err != nil {
		return nil, err
	}
	var out VolumeFileResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode volume browse file: %w", err)
	}
	return &out, nil
}

func (h *RemoteHost) RemoveImage(ctx context.Context, id string, force bool) ([]dtypes.ImageDeleteResponseItem, error) {
	data, err := h.request(ctx, agents.FrameReqImageRemove, agents.ImageRemoveReq{ID: id, Force: force})
	if err != nil {
		return nil, err
	}
	var out []dtypes.ImageDeleteResponseItem
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode image remove: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) PruneImages(ctx context.Context) (dtypes.ImagesPruneReport, error) {
	data, err := h.request(ctx, agents.FrameReqImagePrune, nil)
	if err != nil {
		return dtypes.ImagesPruneReport{}, err
	}
	var out dtypes.ImagesPruneReport
	if err := json.Unmarshal(data, &out); err != nil {
		return dtypes.ImagesPruneReport{}, fmt.Errorf("decode image prune: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) ScaleService(ctx context.Context, name, composeYAML, envContent, service string, replicas int) (*compose.ScaleResult, error) {
	data, err := h.request(ctx, agents.FrameReqStackScale, agents.StackScaleReq{
		Name: name, Compose: composeYAML, Env: envContent,
		Service: service, Replicas: replicas,
	})
	if err != nil {
		return nil, err
	}
	var out compose.ScaleResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode scale result: %w", err)
	}
	return &out, nil
}

func (h *RemoteHost) CheckScale(ctx context.Context, name, composeYAML, envContent, service string) (*compose.ScaleCheck, error) {
	data, err := h.request(ctx, agents.FrameReqStackCheckScale, agents.StackCheckScaleReq{
		Name: name, Compose: composeYAML, Env: envContent, Service: service,
	})
	if err != nil {
		return nil, err
	}
	var out compose.ScaleCheck
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode check scale: %w", err)
	}
	return &out, nil
}

// RollingReplace is not yet implemented on remote hosts — the agent
// protocol does not carry a rolling-update frame type. Callers get a
// clear error so the HTTP handler can surface a 501 rather than a
// generic "frame not handled" from the agent. P.12.5b scope-cut.
func (h *RemoteHost) RollingReplace(ctx context.Context, name, composeYAML, envContent, service string, opts compose.RollingOptions) (*compose.RollingResult, error) {
	return nil, fmt.Errorf("rolling updates on remote hosts are not yet implemented — the agent needs a matching frame type (follow-up slice). Run the stack on the dockmesh server host for now")
}

// Errors
var (
	ErrAgentOffline = errors.New("agent offline")
	ErrNoDocker     = errors.New("docker daemon unavailable")
	ErrUnknownHost  = errors.New("unknown host")
)
