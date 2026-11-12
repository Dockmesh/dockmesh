package updater

import (
	"errors"
)

// Labels Docker sets on the containers of a Swarm service's tasks.
const (
	LabelSwarmServiceID   = "com.docker.swarm.service.id"
	LabelSwarmServiceName = "com.docker.swarm.service.name"
)

// SwarmService returns the name of the Swarm service a task container
// belongs to, its id when the name label is missing, and "" for every
// other container. Swarm replaces a task container that is stopped or
// removed, so a swap would leave a second copy running outside Swarm.
func SwarmService(labels map[string]string) string {
	id := labels[LabelSwarmServiceID]
	if id == "" {
		return ""
	}
	if name := labels[LabelSwarmServiceName]; name != "" {
		return name
	}
	return id
}

// SwarmManagedError refuses an image update or recreate of a Swarm task
// container.
type SwarmManagedError struct {
	Service string
}

func (e *SwarmManagedError) Error() string {
	return "Managed by Swarm service " + e.Service + "."
}

// AsSwarmManaged reports the Swarm service named in err, if any.
func AsSwarmManaged(err error) (string, bool) {
	var e *SwarmManagedError
	if errors.As(err, &e) {
		return e.Service, true
	}
	return "", false
}

func refuseSwarmTask(labels map[string]string) error {
	if svc := SwarmService(labels); svc != "" {
		return &SwarmManagedError{Service: svc}
	}
	return nil
}
