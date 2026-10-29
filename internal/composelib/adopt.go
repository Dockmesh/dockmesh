//go:build composelib

package composelib

import (
	"context"
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"
)

// legacyManagedLabel is set by internal/compose on every container it
// creates; containers without it belong to someone else.
const legacyManagedLabel = "dockmesh.managed"

// AdoptLegacy removes containers the hand-written reconciler created for
// proj. They carry the project/service labels but no container-number, so
// the SDK sees them as replica #0 and fails creating #1 under the same
// name. Named volumes are kept. Returns the removed names.
func (e *Engine) AdoptLegacy(ctx context.Context, proj *types.Project) ([]string, error) {
	list, err := e.api.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", api.ProjectLabel+"="+proj.Name),
	})
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, c := range list.Items {
		if c.Labels[legacyManagedLabel] != "true" {
			continue
		}
		if _, ok := c.Labels[api.ContainerNumberLabel]; ok {
			continue
		}
		if c.Labels[api.OneoffLabel] == "True" {
			continue
		}
		if _, err := e.api.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			return removed, fmt.Errorf("remove legacy container %s: %w", c.ID[:12], err)
		}
		if len(c.Names) > 0 {
			removed = append(removed, c.Names[0])
		}
	}
	return removed, nil
}
