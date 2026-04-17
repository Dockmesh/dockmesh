// Package agents implements remote agent enrollment + the WebSocket
// control plane (concept §3.1, §15.3). The wire format is JSON frames
// over a single bidirectional WebSocket inside an mTLS connection.
//
// Slice 3.1.1 ships only the connection lifecycle: hello / welcome /
// heartbeat / ping. Subsequent slices add request/response frames for
// container ops, exec, logs and stats.
package agents

import (
	"encoding/json"
)

// Frame is the message envelope that flows in both directions.
type Frame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

const (
	// Lifecycle (agent → server)
	FrameAgentHello     = "agent.hello"
	FrameAgentHeartbeat = "agent.heartbeat"
	FrameAgentPong      = "agent.pong"

	// Lifecycle (server → agent)
	FrameServerWelcome = "server.welcome"
	FrameServerPing    = "server.ping"

	// Container operations (server → agent: req.*, agent → server: res.*).
	// Each request carries a unique ID; the response echoes that ID so the
	// server can correlate it with the waiting goroutine.
	FrameReqContainerList    = "req.containers.list"
	FrameReqContainerInspect = "req.containers.inspect"
	FrameReqContainerStart   = "req.containers.start"
	FrameReqContainerStop    = "req.containers.stop"
	FrameReqContainerRestart = "req.containers.restart"
	FrameReqContainerRemove  = "req.containers.remove"
	// P.11.4 container lifecycle additions.
	FrameReqContainerPause   = "req.containers.pause"
	FrameReqContainerUnpause = "req.containers.unpause"
	FrameReqContainerKill    = "req.containers.kill"

	// Resource listings + mutations (server → agent)
	FrameReqImageList   = "req.images.list"
	FrameReqImageRemove = "req.images.remove"
	FrameReqImagePrune  = "req.images.prune"
	FrameReqNetworkList    = "req.networks.list"
	FrameReqNetworkInspect = "req.networks.inspect"
	FrameReqVolumeList     = "req.volumes.list"
	FrameReqVolumeInspect  = "req.volumes.inspect"
	// Volume content browsing (P.11.8). Read-only: list one directory
	// level or read a single file capped at the caller's maxBytes.
	// The stream frame for large-file download is deferred — the UI
	// offers "too large, 5.2 MB" + a download button today.
	FrameReqVolumeBrowse     = "req.volume.browse"
	FrameReqVolumeBrowseFile = "req.volume.browse_file"
	FrameReqDaemonInfo  = "req.daemon.info"

	// Host-level system metrics (CPU / memory / disk / uptime) for the
	// dashboard's all-mode System Health panel. Payload is empty; the
	// agent reads its local /proc and /var/lib/docker via the system
	// package and returns the Metrics struct as JSON. Slice P.6.
	FrameReqSystemMetrics = "req.system.metrics"

	// Stack operations (slice 3.1.3) — server ships compose YAML + .env
	// content with the deploy request, agent runs the same compose
	// executor as the server would for a local deploy.
	FrameReqStackDeploy = "req.stack.deploy"
	FrameReqStackStop   = "req.stack.stop"
	FrameReqStackStatus = "req.stack.status"

	// Agent self-upgrade (Polish). Server pushes a download URL and
	// expected version; agent downloads, replaces its own binary, and
	// restarts the systemd service.
	FrameReqAgentUpgrade = "req.agent.upgrade"

	// Service scaling (P.8) — manual replica count adjustment.
	FrameReqStackScale      = "req.stack.scale"
	FrameReqStackCheckScale = "req.stack.check_scale"

	// Volume tar-stream (P.9 migration). The server opens a stream to
	// the source agent for tar-export (read), and another to the target
	// agent for tar-import (write). The server relays bytes between them.
	FrameReqVolumeTarExport = "req.volume.tar_export" // source: start tar czf - on volume
	FrameReqVolumeTarImport = "req.volume.tar_import" // target: start tar xzf - into volume

	// Compose-file mirroring (P.7) — server pushes the canonical
	// compose+env content to the agent after a successful deploy so
	// each agent retains a local copy in case the main server is lost.
	// stack.delete removes the local copy when the stack is torn down.
	FrameReqStackSync   = "req.stack.sync"
	FrameReqStackDelete = "req.stack.delete"

	// Single response type. Errors set OK=false and put the message in Error.
	FrameRes = "res"

	// Stream multiplexing — one bidirectional logical stream per stream_id,
	// carried over the same agent connection. Used for logs, stats and exec
	// where request/response doesn't fit (long-lived, chunked, possibly
	// bidirectional).
	FrameStreamOpen    = "stream.open"    // server → agent: start a new stream
	FrameStreamData    = "stream.data"    // bidirectional: payload bytes
	FrameStreamClose   = "stream.close"   // bidirectional: end the stream
	FrameStreamControl = "stream.control" // bidirectional: out-of-band ops (resize, signals)
)

// StreamOpen is the server → agent payload that requests a new stream.
// Kind picks the data source on the agent side. Params are kind-specific
// (e.g. {"tail":"100"} for logs).
type StreamOpen struct {
	StreamID  string         `json:"stream_id"`
	Kind      string         `json:"kind"` // "logs" | "stats" | "exec"
	Container string         `json:"container"`
	Params    map[string]any `json:"params,omitempty"`
}

// StreamData carries an opaque chunk of stream payload. Bytes are
// base64-encoded by default JSON marshalling so we can ship raw binary
// (docker multiplexed log frames, etc.) without escaping issues.
type StreamData struct {
	StreamID string `json:"stream_id"`
	Data     []byte `json:"data"`
}

// StreamClose terminates a stream. Error is non-empty if the stream ended
// because of a failure (otherwise it's a clean EOF).
type StreamClose struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

// StreamControl carries out-of-band events that don't fit the byte-oriented
// data channel. Used by exec for tty resize: Op="resize", Params={cols,rows}.
type StreamControl struct {
	StreamID string         `json:"stream_id"`
	Op       string         `json:"op"`
	Params   map[string]any `json:"params,omitempty"`
}

// ResponseEnvelope is the wire format every response uses. Data is the
// JSON-encoded result of the operation; both sides use the same docker
// SDK so types match without translation.
type ResponseEnvelope struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Request payloads — kept tiny, most operations only need an ID + maybe
// a flag.

type ContainerListReq struct {
	All bool `json:"all"`
}

type ContainerIDReq struct {
	ID    string `json:"id"`
	Force bool   `json:"force,omitempty"`
}

// ContainerKillReq carries the target id plus the optional signal name.
// Empty signal is treated by Docker as SIGKILL (its default for Kill).
type ContainerKillReq struct {
	ID     string `json:"id"`
	Signal string `json:"signal,omitempty"`
}

// AgentUpgradeReq tells the agent to download a new binary and restart.
type AgentUpgradeReq struct {
	BinaryURL string `json:"binary_url"` // https://server/install/dockmesh-agent-linux-amd64
	Version   string `json:"version"`    // expected version after upgrade
}

type ResourceIDReq struct {
	ID string `json:"id"`
}

type ImageRemoveReq struct {
	ID    string `json:"id"`
	Force bool   `json:"force,omitempty"`
}

// StackDeployReq carries the full compose project payload to the agent.
// The agent writes Compose + Env to a temp directory, parses with
// compose-go and runs the same DeployProject executor the server uses.
type StackDeployReq struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	Env     string `json:"env,omitempty"`
}

type StackNameReq struct {
	Name string `json:"name"`
}

// StackScaleReq carries the compose content alongside the scale
// request so the agent can parse the project locally. P.8.
type StackScaleReq struct {
	Name     string `json:"name"`
	Compose  string `json:"compose"`
	Env      string `json:"env,omitempty"`
	Service  string `json:"service"`
	Replicas int    `json:"replicas"`
}

// StackCheckScaleReq is the pre-flight check variant. P.8.
type StackCheckScaleReq struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	Env     string `json:"env,omitempty"`
	Service string `json:"service"`
}

// VolumeTarReq identifies the volume for tar export/import.
type VolumeTarReq struct {
	Volume string `json:"volume"`
}

// VolumeBrowseReq is the payload for directory-listing / file-read
// requests (P.11.8). SubPath is relative to the volume root; empty or
// "/" means the root itself. MaxBytes caps the read-file response —
// callers pass 0 for the 1 MiB default.
type VolumeBrowseReq struct {
	Volume   string `json:"volume"`
	SubPath  string `json:"sub_path,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// StackSyncReq carries the full compose + env + optional meta for
// local storage on the agent. Used by compose-file mirroring (P.7).
type StackSyncReq struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	Env     string `json:"env,omitempty"`
	Meta    string `json:"meta,omitempty"` // .dockmesh.meta.json content
}

// HelloPayload is what the agent sends as soon as the WS opens. It tells
// the server which version, OS and docker daemon are on the other end.
type HelloPayload struct {
	Version       string `json:"version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Hostname      string `json:"hostname"`
	DockerVersion string `json:"docker_version,omitempty"`
}

// WelcomePayload is the server's reply confirming the agent is registered.
type WelcomePayload struct {
	AgentID       string `json:"agent_id"`
	AgentName     string `json:"agent_name"`
	ServerVersion string `json:"server_version"`
}

// HeartbeatPayload is sent from the agent on a fixed interval (15s).
type HeartbeatPayload struct {
	TS             int64 `json:"ts"`
	ContainerCount int32 `json:"container_count"`
}

// PingPayload is the server's reverse healthcheck.
type PingPayload struct {
	TS int64 `json:"ts"`
}

// EnrollRequest is POSTed to /api/v1/agents/enroll by an unauthenticated
// agent that wants to swap a one-time token for a client cert.
type EnrollRequest struct {
	Token string `json:"token"`
	// Hostinfo so the server can populate the row on first enrol.
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Version       string `json:"version"`
	DockerVersion string `json:"docker_version,omitempty"`
}

// EnrollResponse contains the freshly-signed client cert + the CA cert
// the agent must trust to validate the server.
type EnrollResponse struct {
	AgentID    string `json:"agent_id"`
	AgentName  string `json:"agent_name"`
	ClientCert string `json:"client_cert"` // PEM
	ClientKey  string `json:"client_key"`  // PEM
	CACert     string `json:"ca_cert"`     // PEM
	AgentURL   string `json:"agent_url"`   // wss URL the agent should dial
}
