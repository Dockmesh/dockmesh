package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/system"
)

// LiveAgents resolves an agent id to its current session, or nil.
type LiveAgents interface {
	GetConnected(id string) *agents.ConnectedAgent
}

// RemoteHost proxies operations to a connected agent over its WebSocket
// using the request/response protocol in internal/agents. The session is
// looked up on every call, so a RemoteHost held across a reconnect talks
// to the new session instead of a dead one.
type RemoteHost struct {
	id     string
	name   string
	agents LiveAgents

	afterDeploy DeployHook
}

func NewRemote(id, name string, live LiveAgents) *RemoteHost {
	return &RemoteHost{id: id, name: name, agents: live}
}

func (h *RemoteHost) live() (*agents.ConnectedAgent, error) {
	if h.agents == nil {
		return nil, ErrAgentOffline
	}
	if ag := h.agents.GetConnected(h.id); ag != nil {
		return ag, nil
	}
	return nil, ErrAgentOffline
}

func (h *RemoteHost) ID() string   { return h.id }
func (h *RemoteHost) Name() string { return h.name }

func (h *RemoteHost) request(ctx context.Context, frameType string, payload any) (json.RawMessage, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	env, err := ag.Request(ctx, agents.Frame{Type: frameType, Payload: raw})
	if err != nil {
		return nil, err
	}
	return env.Data, nil
}

func (h *RemoteHost) ListContainers(ctx context.Context, all bool) ([]container.Summary, error) {
	data, err := h.request(ctx, agents.FrameReqContainerList, agents.ContainerListReq{All: all})
	if err != nil {
		return nil, err
	}
	var out []container.Summary
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode containers: %w", err)
	}
	if out == nil {
		out = []container.Summary{}
	}
	return out, nil
}

func (h *RemoteHost) InspectContainer(ctx context.Context, id string) (container.InspectResponse, error) {
	data, err := h.request(ctx, agents.FrameReqContainerInspect, agents.ContainerIDReq{ID: id})
	if err != nil {
		return container.InspectResponse{}, err
	}
	var out container.InspectResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return container.InspectResponse{}, fmt.Errorf("decode inspect: %w", err)
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
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	stream, err := ag.OpenStream(ctx, "logs", id, map[string]any{
		"tail":   tail,
		"follow": follow,
	})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func (h *RemoteHost) ContainerStats(ctx context.Context, id string) (io.ReadCloser, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	return ag.OpenStream(ctx, "stats", id, nil)
}

func (h *RemoteHost) StartExec(ctx context.Context, id string, cmd []string) (ExecSession, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	stream, err := ag.OpenStream(ctx, "exec", id, map[string]any{
		"cmd": cmd,
		// Sensible TTY defaults; the browser sends a resize as soon as it
		// has measured xterm.fit so this rarely shows.
		"cols": 80,
		"rows": 24,
	})
	if err != nil {
		return nil, err
	}
	// A stdin write waiting for credit only ends when the stream does, so
	// the caller's cancel has to close it.
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() }) //nolint:contextcheck // runs after ctx is done
	return &remoteExecSession{stream: stream, stop: stop}, nil
}

// remoteExecSession adapts an agent stream to the ExecSession interface.
// Reads pull from the stream's incoming buffer (stdout). Writes push
// stream.data frames in the server → agent direction (stdin). Resize
// uses an out-of-band stream.control frame.
type remoteExecSession struct {
	stream *agents.Stream
	stop   func() bool
}

func (s *remoteExecSession) Read(p []byte) (int, error) { return s.stream.Read(p) }
func (s *remoteExecSession) Write(p []byte) (int, error) {
	if err := s.stream.WriteFrame(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (s *remoteExecSession) Resize(rows, cols uint) error {
	return s.stream.WriteControl("resize", map[string]any{"cols": cols, "rows": rows})
}
func (s *remoteExecSession) Close() error {
	s.stop()
	return s.stream.Close()
}

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
	if h.afterDeploy != nil {
		h.afterDeploy(ctx, h.id, name, &out)
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

func (h *RemoteHost) ListImages(ctx context.Context, all bool) ([]image.Summary, error) {
	data, err := h.request(ctx, agents.FrameReqImageList, map[string]bool{"all": all})
	if err != nil {
		return nil, err
	}
	var out []image.Summary
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode images: %w", err)
	}
	if out == nil {
		out = []image.Summary{}
	}
	return out, nil
}

// InspectImage asks the agent for one image's full ImageInspect record.
// Same shape as Docker SDK's ImageInspect on the local daemon,
// just round-tripped over the agent WebSocket. Returns the docker
// "no such image" error verbatim when missing so the handler can map
// to 404.
func (h *RemoteHost) InspectImage(ctx context.Context, id string) (image.InspectResponse, error) {
	data, err := h.request(ctx, agents.FrameReqImageInspect, agents.ResourceIDReq{ID: id})
	if err != nil {
		return image.InspectResponse{}, err
	}
	var out image.InspectResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return image.InspectResponse{}, fmt.Errorf("decode image inspect: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) ListNetworks(ctx context.Context) ([]network.Inspect, error) {
	data, err := h.request(ctx, agents.FrameReqNetworkList, nil)
	if err != nil {
		return nil, err
	}
	var out []network.Inspect
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode networks: %w", err)
	}
	if out == nil {
		out = []network.Inspect{}
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
	mounts, err := h.DiskMounts(ctx)
	if errors.Is(err, ErrMountsUnsupported) {
		out.MountsUnsupported = true
		return out, nil
	}
	if err != nil {
		slog.Warn("agent disk mounts", "host", h.name, "err", err)
		return out, nil
	}
	return system.ApplyMounts(out, mounts), nil
}

// DiskMounts returns the agent's per-mount disk usage.
func (h *RemoteHost) DiskMounts(ctx context.Context) ([]system.Mount, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	if !ag.Hello.Has(agents.CapDiskMounts) {
		return nil, ErrMountsUnsupported
	}
	data, err := h.request(ctx, agents.FrameReqSystemDisks, nil)
	if err != nil {
		return nil, err
	}
	var out []system.Mount
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode disk mounts: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) InspectNetwork(ctx context.Context, id string) (network.Inspect, error) {
	data, err := h.request(ctx, agents.FrameReqNetworkInspect, agents.ResourceIDReq{ID: id})
	if err != nil {
		return network.Inspect{}, err
	}
	var out network.Inspect
	if err := json.Unmarshal(data, &out); err != nil {
		return network.Inspect{}, fmt.Errorf("decode network inspect: %w", err)
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
// browse frames. The agent runs the same BrowseVolumeOnHost /
// ReadVolumeFileOnHost helpers against its own docker daemon.
func (h *RemoteHost) VolumeBrowseEntries(ctx context.Context, name, subpath string) ([]VolumeEntry, error) {
	data, err := h.request(ctx, agents.FrameReqVolumeBrowse, agents.VolumeBrowseReq{Volume: name, SubPath: subpath})
	if err != nil {
		return nil, agentBrowseError(err)
	}
	var out []VolumeEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode volume browse: %w", err)
	}
	return out, nil
}

// VolumeTar opens a volume_export stream on the remote agent and
// returns an io.ReadCloser of the tar.gz bytes.
// The agent ships raw docker hijack bytes (with 8-byte mux headers)
// so we wrap the stream in a demuxer to recover stdout-only content.
func (h *RemoteHost) VolumeTar(ctx context.Context, name string) (io.ReadCloser, error) {
	ag, err := h.live()
	if err != nil {
		return nil, err
	}
	s, err := ag.OpenStream(ctx, "volume_export", "", map[string]any{"volume": name})
	if err != nil {
		return nil, fmt.Errorf("open volume_export stream: %w", err)
	}
	return &dockerMuxStripper{src: s}, nil
}

// ErrAgentTooOld is returned when the agent can't take a volume restore
// (or not the requested mode); it needs the agent version that ships with
// this server.
var ErrAgentTooOld = errors.New("the agent on this host is too old to restore volumes; upgrade it from the Hosts page")

// VolumeImport streams a tar.gz archive into the named volume on the
// agent's host. replace swaps the volume's contents for the archive's
// once it extracted completely, needing needBytes of free space; only
// agents that extract into a staging directory take it. Returns tar's
// result.
//
//nolint:contextcheck // stream writes have no ctx; the AfterFunc below closes the stream when ctx ends
func (h *RemoteHost) VolumeImport(ctx context.Context, name string, src io.Reader, replace bool, needBytes int64) error {
	ag, err := h.live()
	if err != nil {
		return err
	}
	// Without flow control the agent drops input that arrives faster than
	// tar reads it; an older agent's replace empties the volume before
	// any data arrives.
	if !ag.Hello.Has(agents.CapStreamCredit) || (replace && !ag.Hello.Has(agents.CapVolumeStaged)) {
		return ErrAgentTooOld
	}
	if err := docker.CheckVolumeName(name); err != nil {
		return err
	}
	s, err := ag.OpenStream(ctx, "volume_import", "", map[string]any{"volume": name, "replace": replace, "need_bytes": needBytes})
	if err != nil {
		return fmt.Errorf("open volume_import stream: %w", err)
	}
	defer s.Close()
	// A write waiting for credit only ends when the stream does.
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	stall := time.AfterFunc(importStall, func() { _ = s.Close() })
	defer stall.Stop()

	buf := make([]byte, 32*1024)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if err := s.WriteFrame(append([]byte(nil), buf[:n]...)); err != nil {
				return fmt.Errorf("send archive: %w", err)
			}
			// An agent that stops taking data fails the restore instead
			// of holding it open forever.
			stall.Reset(importStall)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return fmt.Errorf("read archive: %w", rerr)
		}
	}
	stall.Stop()
	if err := s.CloseWrite(); err != nil {
		return fmt.Errorf("finish upload: %w", err)
	}
	// The agent closes the stream once tar has exited; its error, if
	// any, is what Read returns.
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, s)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("volume import on agent: %w", err)
		}
		return nil
	case <-time.After(importStall):
		return errors.New("volume import on agent: no result from tar")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// importStall bounds how long a volume import may make no progress.
const importStall = 5 * time.Minute

// dockerMuxStripper demuxes docker's attach-stream format: repeating
//
//	[stream_id(1), 0, 0, 0, size(4 BE)] + <size bytes>
//
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

// ContainerExec runs a command in the remote container and blocks until
// it exits. Used by backup pre-hooks.
func (h *RemoteHost) ContainerExec(ctx context.Context, containerID string, cmd []string) ([]byte, int, error) {
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
		return nil, agentBrowseError(err)
	}
	var out VolumeFileResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode volume browse file: %w", err)
	}
	return &out, nil
}

// agentError keeps the agent's message but matches the sentinel it
// carried, so errors.Is works across the wire.
type agentError struct {
	err  error
	kind error
}

func (e *agentError) Error() string   { return e.err.Error() }
func (e *agentError) Unwrap() []error { return []error{e.kind, e.err} }

// agentBrowseError recovers the browse sentinels from the plain-text
// error an agent returns. Matching on the message works with every agent
// version, since all of them send err.Error() of the same sentinels.
func agentBrowseError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, kind := range []error{
		ErrVolumePathEscape,
		ErrVolumePathTooLong,
		ErrVolumeNotDir,
		ErrVolumeNotFile,
		ErrVolumeMountpointMissing,
	} {
		if strings.Contains(msg, kind.Error()) {
			return &agentError{err: err, kind: kind}
		}
	}
	if strings.Contains(msg, fs.ErrNotExist.Error()) || strings.Contains(msg, "no such file or directory") {
		return &agentError{err: err, kind: fs.ErrNotExist}
	}
	return err
}

func (h *RemoteHost) RemoveImage(ctx context.Context, id string, force bool) ([]image.DeleteResponse, error) {
	data, err := h.request(ctx, agents.FrameReqImageRemove, agents.ImageRemoveReq{ID: id, Force: force})
	if err != nil {
		return nil, err
	}
	var out []image.DeleteResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode image remove: %w", err)
	}
	return out, nil
}

func (h *RemoteHost) PruneImages(ctx context.Context) (image.PruneReport, error) {
	data, err := h.request(ctx, agents.FrameReqImagePrune, nil)
	if err != nil {
		return image.PruneReport{}, err
	}
	var out image.PruneReport
	if err := json.Unmarshal(data, &out); err != nil {
		return image.PruneReport{}, fmt.Errorf("decode image prune: %w", err)
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

// Container file-browser ops aren't carried by the agent protocol yet —
// the central server can't CopyFromContainer against a remote daemon
// without a matching frame type. Return clear errors so handlers 501
// rather than emitting a confusing "frame not handled".
func (h *RemoteHost) ContainerBrowseEntries(ctx context.Context, id, p string) ([]VolumeEntry, error) {
	return nil, fmt.Errorf("browsing container files is available on the server host only; agent hosts follow in a later release")
}

func (h *RemoteHost) ContainerReadFile(ctx context.Context, id, p string, maxBytes int64) (*VolumeFileResult, error) {
	return nil, fmt.Errorf("browsing container files is available on the server host only; agent hosts follow in a later release")
}

func (h *RemoteHost) ContainerDownloadFile(ctx context.Context, id, p string) (io.ReadCloser, string, int64, error) {
	return nil, "", 0, fmt.Errorf("browsing container files is available on the server host only; agent hosts follow in a later release")
}

func (h *RemoteHost) ContainerWriteFile(ctx context.Context, id, p string, data []byte, mode int64) error {
	return fmt.Errorf("browsing container files is available on the server host only; agent hosts follow in a later release")
}

// Errors
var (
	ErrAgentOffline = errors.New("agent offline")
	ErrNoDocker     = errors.New("docker daemon unavailable")
	ErrUnknownHost  = errors.New("unknown host")
)
