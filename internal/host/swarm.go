package host

import (
	"context"

	"github.com/dockmesh/dockmesh/internal/swarmtask"
)

// refuseTaskRestart refuses a restart of a Swarm task container: Docker
// brings the restarted container back up while Swarm already starts a
// replacement task, so every restart leaves one extra copy running.
func refuseTaskRestart(ctx context.Context, h Host, id string) error {
	info, err := h.InspectContainer(ctx, id)
	if err != nil {
		return err
	}
	if info.Config == nil {
		return nil
	}
	return swarmtask.Refuse(info.Config.Labels)
}
