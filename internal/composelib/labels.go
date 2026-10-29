//go:build composelib

package composelib

import (
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
)

// StampLabels sets the per-service labels the SDK's own LoadProject would
// add. Containers get their project/service labels from these, so a
// project loaded through internal/compose.LoadProject (which, unlike the
// SDK loader, never interpolates the server's OS env or reads .env from
// disk) is invisible to Ps/Down and `docker compose` without them.
func StampLabels(proj *types.Project) {
	for name, s := range proj.Services {
		s.CustomLabels = map[string]string{
			api.ProjectLabel:     proj.Name,
			api.ServiceLabel:     name,
			api.VersionLabel:     api.ComposeVersion,
			api.WorkingDirLabel:  proj.WorkingDir,
			api.ConfigFilesLabel: strings.Join(proj.ComposeFiles, ","),
			api.OneoffLabel:      "False",
		}
		proj.Services[name] = s
	}
}
