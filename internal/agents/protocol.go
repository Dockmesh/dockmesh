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

	// Resource listings (read-only, server → agent)
	FrameReqImageList   = "req.images.list"
	FrameReqNetworkList = "req.networks.list"
	FrameReqVolumeList  = "req.volumes.list"
	FrameReqDaemonInfo  = "req.daemon.info"

	// Stack operations (slice 3.1.3) — server ships compose YAML + .env
	// content with the deploy request, agent runs the same compose
	// executor as the server would for a local deploy.
	FrameReqStackDeploy = "req.stack.deploy"
	FrameReqStackStop   = "req.stack.stop"
	FrameReqStackStatus = "req.stack.status"

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
