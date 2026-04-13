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
	// Agent → Server
	FrameAgentHello     = "agent.hello"
	FrameAgentHeartbeat = "agent.heartbeat"
	FrameAgentPong      = "agent.pong"

	// Server → Agent
	FrameServerWelcome = "server.welcome"
	FrameServerPing    = "server.ping"
)

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
