// Package swarmtask recognises the containers Swarm runs for a service's
// tasks. Dockmesh never updates, recreates or restarts them: Swarm
// replaces a task container that is stopped or removed, so any of those
// would leave a second copy running outside Swarm's desired state.
package swarmtask

import "errors"

// Labels Docker sets on the containers of a Swarm service's tasks.
const (
	LabelServiceID   = "com.docker.swarm.service.id"
	LabelServiceName = "com.docker.swarm.service.name"
)

// Service returns the name of the Swarm service a task container belongs
// to, its id when the name label is missing, and "" for every other
// container.
func Service(labels map[string]string) string {
	id := labels[LabelServiceID]
	if id == "" {
		return ""
	}
	if name := labels[LabelServiceName]; name != "" {
		return name
	}
	return id
}

// ManagedError refuses an action on a Swarm task container.
type ManagedError struct {
	Service string
}

func (e *ManagedError) Error() string {
	return "Managed by Swarm service " + e.Service + "."
}

// AsManaged reports the Swarm service named in err, if any.
func AsManaged(err error) (string, bool) {
	var e *ManagedError
	if errors.As(err, &e) {
		return e.Service, true
	}
	return "", false
}

// Refuse returns a *ManagedError for a task container and nil otherwise.
func Refuse(labels map[string]string) error {
	if svc := Service(labels); svc != "" {
		return &ManagedError{Service: svc}
	}
	return nil
}
