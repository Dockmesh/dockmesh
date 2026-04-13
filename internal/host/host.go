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

	dtypes "github.com/docker/docker/api/types"
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

	// Resource lists (read-only — full CRUD comes later)
	ListImages(ctx context.Context, all bool) ([]dtypes.ImageSummary, error)
	ListNetworks(ctx context.Context) ([]dtypes.NetworkResource, error)
	ListVolumes(ctx context.Context) ([]any, error)
}

// Info is what /api/v1/hosts returns for the frontend host switcher.
type Info struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`   // "local" | "agent"
	Status string `json:"status"` // "online" | "offline"
}
