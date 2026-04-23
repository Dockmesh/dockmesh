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

// VolumeTar opens a volume_export stream on the remote agent and
// returns an io.ReadCloser of the tar.gz bytes. FINDING-33.
// The agent ships raw docker hijack bytes (with 8-byte mux headers)
// so we wrap the stream in a demuxer to recover stdout-only content.
func (h *RemoteHost) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	if h.agent == nil {
		return nil, fmt.Errorf("agent connection unavailable")
	}
	s, err := h.agent.OpenStream(ctx, "volume_export", "", map[string]any{"volume": name})
	if err != nil {
		return nil, fmt.Errorf("open volume_export stream: %w", err)
	}
	return &dockerMuxStripper{src: s}, nil
}

// dockerMuxStripper demuxes docker's attach-stream format: repeating
//   [stream_id(1), 0, 0, 0, size(4 BE)] + <size bytes>
// across arbitrary chunk boundaries. Only stream_id=1 (stdout) is
// forwarded; stderr (stream_id=2) is discarded. Used by RemoteHost
// backup path.
type dockerMuxStripper struct {
	src       io.ReadCloser
	remaining int  // bytes remaining in the current frame payload
	skipping  bool // true when the current frame is stderr — drop
	hdrBuf    [8]byte
	hdrFill   int
	leftover  []byte
}

func (d *dockerMuxStripper) Read(p []byte) (int, error) {
	if len(d.leftover) > 0 {
		n := copy(p, d.leftover)
		d.leftover = d.leftover[n:]
		return n, nil
	}
	// Need to read next header
	if d.remaining == 0 {
		for d.hdrFill < 8 {
			n, err := d.src.Read(d.hdrBuf[d.hdrFill:])
			d.hdrFill += n
			if err != nil {
				if d.hdrFill == 0 {
					return 0, err
				}
				return 0, io.ErrUnexpectedEOF
			}
		}
		streamID := d.hdrBuf[0]
		size := int(d.hdrBuf[4])<<24 | int(d.hdrBuf[5])<<16 | int(d.hdrBuf[6])<<8 | int(d.hdrBuf[7])
		d.remaining = size
		d.skipping = streamID != 1
		d.hdrFill = 0
		if size == 0 {
			return d.Read(p)
		}
	}
	// Read up to min(remaining, len(p))
	toRead := d.remaining
	if toRead > len(p) {
		toRead = len(p)
	}
	n, err := d.src.Read(p[:toRead])
	d.remaining -= n
	if d.skipping {
		// discard; recurse for stdout bytes
		if err != nil {
			return 0, err
		}
		return d.Read(p)
	}
	return n, err
}

func (d *dockerMuxStripper) Close() error {
	return d.src.Close()
}

// ContainerExec runs a command in the remote container via a new frame.
// Blocks until exit. Used by backup pre-hooks. The agent can already
// exec via the "exec" stream (interactive TTY) but we want request/
// response semantics here; piggy-back on the existing exec-stream but
// wait for it to close.
func (h *RemoteHost) ContainerExec(ctx context.Context, containerID string, cmd []string) ([]byte, int, error) {
	if h.agent == nil {
		return nil, -1, fmt.Errorf("agent connection unavailable")
	}
	data, err := h.request(ctx, agents.FrameReqContainerExecRun,
		agents.ContainerExecRunReq{Container: containerID, Cmd: cmd, MaxOutputBytes: 1 << 20})
	if err != nil {
		return nil, -1, err
	}
	var res agents.ContainerExecRunRes
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, -1, err
	}
	return res.Stdout, res.ExitCode, nil
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

// CleanupStack / CleanupPreview are not yet wired through the agent
// protocol. Return a clear error so the HTTP handler can 501 instead of
// silently no-op'ing. The UI disables the matching checkboxes when the
// stack is on a remote host.
func (h *RemoteHost) CleanupStack(ctx context.Context, name string, opts compose.CleanupOpts) (*compose.CleanupResult, error) {
	return nil, fmt.Errorf("resource cleanup on remote hosts is not yet implemented — the agent needs matching frame types (follow-up slice)")
}

func (h *RemoteHost) CleanupPreview(ctx context.Context, name string) (*compose.CleanupPlan, error) {
	return nil, fmt.Errorf("resource cleanup on remote hosts is not yet implemented — the agent needs matching frame types (follow-up slice)")
}

// Errors
var (
	ErrAgentOffline = errors.New("agent offline")
	ErrNoDocker     = errors.New("docker daemon unavailable")
	ErrUnknownHost  = errors.New("unknown host")
)
