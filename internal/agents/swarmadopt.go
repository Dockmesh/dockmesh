package agents

import (
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
)

// SwarmExportReq asks a manager for everything a stack deployed with
// `docker stack deploy` consists of, so the server can write it down as a
// compose file.
type SwarmExportReq struct {
	Stack string `json:"stack"`
}

// SwarmExportResp carries the full specs, environment values included.
// It only ever goes to the server, which needs them to adopt the stack.
type SwarmExportResp struct {
	Services []swarm.Service   `json:"services"`
	Networks []network.Summary `json:"networks"`
}

// SwarmAdoptReq marks the services of an External stack as Dockmesh's by
// adding the spec hash label, nothing else. Version is the one the
// server read: a service changed since is refused, so the label lands on
// exactly the spec the caller confirmed.
type SwarmAdoptReq struct {
	Stack    string              `json:"stack"`
	Services []SwarmAdoptService `json:"services"`
}

type SwarmAdoptService struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  uint64 `json:"version"`
	SpecHash string `json:"spec_hash"`
}

type SwarmAdoptResp struct {
	Services []SwarmWriteResult `json:"services"`
}

// Validate checks an export request.
func (r SwarmExportReq) Validate() error {
	if !ValidStackName(r.Stack) {
		return refuse("invalid stack name")
	}
	return nil
}

// Validate checks an adopt request: the stack's own service names, each
// once, each with a spec hash.
func (r SwarmAdoptReq) Validate() error {
	if !ValidStackName(r.Stack) {
		return refuse("invalid stack name")
	}
	if len(r.Services) == 0 || len(r.Services) > MaxSwarmWriteServices {
		return refuse("an adopt carries 1 to %d services", MaxSwarmWriteServices)
	}
	seen := map[string]bool{}
	for _, s := range r.Services {
		if !swarmObjName.MatchString(s.Name) || len(s.Name) <= len(r.Stack)+1 || s.Name[:len(r.Stack)+1] != r.Stack+"_" {
			return refuse("service name must start with %s_", r.Stack)
		}
		if s.ID == "" || !swarmObjName.MatchString(s.ID) {
			return refuse("service %s: invalid id", s.Name)
		}
		if !specHashRe.MatchString(s.SpecHash) {
			return refuse("service %s: invalid spec hash", s.Name)
		}
		if seen[s.Name] {
			return refuse("service %s listed twice", s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}
