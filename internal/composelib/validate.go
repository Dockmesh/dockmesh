//go:build composelib

package composelib

import (
	"fmt"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// Validate rejects compose features the SDK implements outside a
// container: provider services and models run host binaries with the stack
// env, use_api_socket copies every registry credential of the call into
// the container, and a seccomp profile path is read from the Dockmesh
// host's filesystem.
func Validate(proj *types.Project) error {
	if len(proj.Models) > 0 {
		return fmt.Errorf("top-level models are not supported")
	}
	for name, s := range proj.Services {
		if s.Provider != nil {
			return fmt.Errorf("service %s: provider services are not supported", name)
		}
		if len(s.Models) > 0 {
			return fmt.Errorf("service %s: models are not supported", name)
		}
		if s.UseAPISocket {
			return fmt.Errorf("service %s: use_api_socket is not supported", name)
		}
		for _, opt := range s.SecurityOpt {
			k, v, _ := strings.Cut(opt, "=")
			if !strings.EqualFold(k, "seccomp") {
				continue
			}
			if v != "unconfined" && v != "builtin" {
				return fmt.Errorf("service %s: seccomp profile files are not supported", name)
			}
		}
	}
	return nil
}
