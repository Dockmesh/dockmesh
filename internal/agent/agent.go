package agent

import "context"

// Agent is the remote-host agent protocol. Phase 3 wires this to a gRPC
// stream where the agent dials outbound — no inbound ports on remote hosts.
type Agent interface {
	ID() string
	Exec(ctx context.Context, cmd Command) (*Result, error)
}

type Command struct {
	Kind string            `json:"kind"`
	Args map[string]string `json:"args"`
}

type Result struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// TODO(phase3): gRPC bidi stream, mTLS, token enrollment.
