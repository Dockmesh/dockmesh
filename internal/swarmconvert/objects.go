package swarmconvert

import (
	"fmt"
	"os"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/swarm"
)

// fileRef is the name, uid, gid and mode a secret or config reference
// mounts its file with, with the CLI's defaults.
func fileRef(ref types.FileReferenceConfig) (name, uid, gid string, mode os.FileMode, err error) {
	name, uid, gid, mode = ref.Target, ref.UID, ref.GID, 0o444
	if name == "" {
		name = ref.Source
	}
	if uid == "" {
		uid = "0"
	}
	if gid == "" {
		gid = "0"
	}
	if ref.Mode != nil {
		if *ref.Mode < 0 || *ref.Mode > 0o7777 {
			return "", "", "", 0, fmt.Errorf("%s: mode %o is not a file mode", ref.Source, *ref.Mode)
		}
		mode = os.FileMode(*ref.Mode)
	}
	return name, uid, gid, mode, nil
}

// convertSecrets references the external secrets svc uses by name. The
// deploy resolves the names to ids on the Swarm. References to secrets
// the stack would own are left out; Plan refuses those.
func convertSecrets(svc types.ServiceConfig, secrets types.Secrets) ([]*swarm.SecretReference, error) {
	var out []*swarm.SecretReference
	for _, ref := range svc.Secrets {
		s, ok := secrets[ref.Source]
		if !ok || !bool(s.External) {
			continue
		}
		name, uid, gid, mode, err := fileRef(types.FileReferenceConfig(ref))
		if err != nil {
			return nil, fmt.Errorf("secrets %w", err)
		}
		// compose-go turns the default target into /run/secrets/<name>; the
		// CLI sends the bare name, which Swarm mounts at the same place.
		if rest, ok := strings.CutPrefix(name, "/run/secrets/"); ok && rest != "" && !strings.Contains(rest, "/") {
			name = rest
		}
		out = append(out, &swarm.SecretReference{
			SecretName: s.Name,
			File:       &swarm.SecretReferenceFileTarget{Name: name, UID: uid, GID: gid, Mode: mode},
		})
	}
	return out, nil
}

// convertConfigs is convertSecrets for configs.
func convertConfigs(svc types.ServiceConfig, configs types.Configs) ([]*swarm.ConfigReference, error) {
	var out []*swarm.ConfigReference
	for _, ref := range svc.Configs {
		c, ok := configs[ref.Source]
		if !ok || !bool(c.External) {
			continue
		}
		name, uid, gid, mode, err := fileRef(types.FileReferenceConfig(ref))
		if err != nil {
			return nil, fmt.Errorf("configs %w", err)
		}
		out = append(out, &swarm.ConfigReference{
			ConfigName: c.Name,
			File:       &swarm.ConfigReferenceFileTarget{Name: name, UID: uid, GID: gid, Mode: mode},
		})
	}
	return out, nil
}
