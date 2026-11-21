package agents

// Actions on one service of a Dockmesh stack.
const (
	SwarmServiceRollback = "rollback"
	SwarmServiceResume   = "resume"
)

// SwarmServiceActionReq runs an action on one service Dockmesh deployed.
// ID is the service the server read, so a service replaced since is
// refused.
type SwarmServiceActionReq struct {
	Stack   string `json:"stack"`
	Service string `json:"service"`
	ID      string `json:"id"`
	Action  string `json:"action"`
}

// Validate checks an action request: the stack's own service name, a
// known action.
func (r SwarmServiceActionReq) Validate() error {
	if !ValidStackName(r.Stack) {
		return refuse("invalid stack name")
	}
	if !swarmObjName.MatchString(r.Service) || len(r.Service) <= len(r.Stack)+1 || r.Service[:len(r.Stack)+1] != r.Stack+"_" {
		return refuse("service name must start with %s_", r.Stack)
	}
	if !swarmObjName.MatchString(r.ID) {
		return refuse("invalid service id")
	}
	switch r.Action {
	case SwarmServiceRollback, SwarmServiceResume:
	default:
		return refuse("unknown action")
	}
	return nil
}
