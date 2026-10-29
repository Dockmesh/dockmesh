//go:build composelib

package composelib

import (
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
)

// StampLabels adds the per-service labels the SDK loader would set.
// internal/compose.LoadProject skips them, and without them Ps/Down and
// `docker compose` cannot see the containers.
func StampLabels(proj *types.Project) {
	for name, s := range proj.Services {
		s.CustomLabels = map[string]string{
			api.ProjectLabel:     proj.Name,
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  proj.WorkingDir,
			api.ConfigFilesLabel: strings.Join(proj.ComposeFiles, ","),
			api.OneoffLabel:      "False",
			legacyManagedLabel:   "true",
		}
		proj.Services[name] = s
	}
}
