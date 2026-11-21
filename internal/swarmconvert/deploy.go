package swarmconvert

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/swarm"
)

func convertMode(deploy *types.DeployConfig) (swarm.ServiceMode, error) {
	var mode string
	var replicas *uint64
	if deploy != nil {
		mode = deploy.Mode
		if deploy.Replicas != nil {
			if *deploy.Replicas < 0 {
				return swarm.ServiceMode{}, fmt.Errorf("deploy.replicas must not be negative")
			}
			n := uint64(*deploy.Replicas)
			replicas = &n
		}
	}
	switch mode {
	case "replicated", "":
		return swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: replicas}}, nil
	case "global":
		if replicas != nil {
			return swarm.ServiceMode{}, fmt.Errorf("deploy.replicas can only be used with replicated or replicated-job mode")
		}
		return swarm.ServiceMode{Global: &swarm.GlobalService{}}, nil
	case "replicated-job":
		return swarm.ServiceMode{ReplicatedJob: &swarm.ReplicatedJob{MaxConcurrent: replicas, TotalCompletions: replicas}}, nil
	case "global-job":
		if replicas != nil {
			return swarm.ServiceMode{}, fmt.Errorf("deploy.replicas can only be used with replicated or replicated-job mode")
		}
		return swarm.ServiceMode{GlobalJob: &swarm.GlobalJob{}}, nil
	}
	return swarm.ServiceMode{}, fmt.Errorf("deploy.mode %q is not replicated, global, replicated-job or global-job", mode)
}

func convertEndpointMode(deploy *types.DeployConfig) (swarm.ResolutionMode, error) {
	if deploy == nil {
		return "", nil
	}
	switch m := swarm.ResolutionMode(strings.ToLower(deploy.EndpointMode)); m {
	case "", swarm.ResolutionModeVIP, swarm.ResolutionModeDNSRR:
		return m, nil
	}
	return "", fmt.Errorf("deploy.endpoint_mode %q is not vip or dnsrr", deploy.EndpointMode)
}

func convertRestartPolicy(deploy *types.DeployConfig) (*swarm.RestartPolicy, error) {
	if deploy == nil || deploy.RestartPolicy == nil {
		return nil, nil
	}
	p := deploy.RestartPolicy
	switch swarm.RestartPolicyCondition(p.Condition) {
	case "", swarm.RestartPolicyConditionNone, swarm.RestartPolicyConditionOnFailure, swarm.RestartPolicyConditionAny:
	default:
		return nil, fmt.Errorf("deploy.restart_policy.condition %q is not none, on-failure or any", p.Condition)
	}
	return &swarm.RestartPolicy{
		Condition:   swarm.RestartPolicyCondition(p.Condition),
		Delay:       durationPtr(p.Delay),
		MaxAttempts: p.MaxAttempts,
		Window:      durationPtr(p.Window),
	}, nil
}

// convertPlacement always returns a Placement, empty when nothing is set,
// as the CLI sends it.
func convertPlacement(deploy *types.DeployConfig) *swarm.Placement {
	out := &swarm.Placement{}
	if deploy == nil {
		return out
	}
	p := deploy.Placement
	out.Constraints = p.Constraints
	out.MaxReplicas = p.MaxReplicas
	for _, pref := range p.Preferences {
		out.Preferences = append(out.Preferences, swarm.PlacementPreference{
			Spread: &swarm.SpreadOver{SpreadDescriptor: pref.Spread},
		})
	}
	return out
}

// convertUpdateConfig maps update_config and rollback_config. Unset
// fields stay zero so the daemon applies its defaults (failure_action
// pause); only parallelism defaults to 1 here, as in the CLI.
func convertUpdateConfig(key string, c *types.UpdateConfig) (*swarm.UpdateConfig, error) {
	if c == nil {
		return nil, nil
	}
	parallel := uint64(1)
	if c.Parallelism != nil {
		parallel = *c.Parallelism
	}
	switch swarm.FailureAction(c.FailureAction) {
	case "", swarm.UpdateFailureActionPause, swarm.UpdateFailureActionContinue, swarm.UpdateFailureActionRollback:
	default:
		return nil, fmt.Errorf("deploy.%s.failure_action %q is not pause, continue or rollback", key, c.FailureAction)
	}
	if key == "rollback_config" && swarm.FailureAction(c.FailureAction) == swarm.UpdateFailureActionRollback {
		return nil, fmt.Errorf("deploy.rollback_config.failure_action can't be rollback")
	}
	return &swarm.UpdateConfig{
		Parallelism:     parallel,
		Delay:           time.Duration(c.Delay),
		FailureAction:   swarm.FailureAction(c.FailureAction),
		Monitor:         time.Duration(c.Monitor),
		MaxFailureRatio: c.MaxFailureRatio,
		Order:           swarm.UpdateOrder(c.Order),
	}, nil
}

// convertResources always returns requirements, empty when nothing is
// set, as the CLI sends them. Devices are left out: a Swarm service has
// no device requests.
func convertResources(deploy *types.DeployConfig) (*swarm.ResourceRequirements, error) {
	out := &swarm.ResourceRequirements{}
	if deploy == nil {
		return out, nil
	}
	r := deploy.Resources
	if r.Limits != nil {
		cpus, err := nanoCPUs(r.Limits.NanoCPUs)
		if err != nil {
			return nil, fmt.Errorf("deploy.resources.limits.cpus: %w", err)
		}
		out.Limits = &swarm.Limit{NanoCPUs: cpus, MemoryBytes: int64(r.Limits.MemoryBytes), Pids: r.Limits.Pids}
	}
	if r.Reservations != nil {
		cpus, err := nanoCPUs(r.Reservations.NanoCPUs)
		if err != nil {
			return nil, fmt.Errorf("deploy.resources.reservations.cpus: %w", err)
		}
		res := &swarm.Resources{NanoCPUs: cpus, MemoryBytes: int64(r.Reservations.MemoryBytes)}
		for _, g := range r.Reservations.GenericResources {
			var out swarm.GenericResource
			if g.DiscreteResourceSpec != nil {
				out.DiscreteResourceSpec = &swarm.DiscreteGenericResource{
					Kind:  g.DiscreteResourceSpec.Kind,
					Value: g.DiscreteResourceSpec.Value,
				}
			}
			res.GenericResources = append(res.GenericResources, out)
		}
		out.Reservations = res
	}
	return out, nil
}

// nanoCPUs converts compose-go's float32 cpus the way the CLI parses the
// written decimal: the shortest float32 form is the decimal of the file,
// so "0.1" stays 100000000 instead of float32 rounding noise.
func nanoCPUs(v types.NanoCPUs) (int64, error) {
	if v == 0 {
		return 0, nil
	}
	if v < 0 {
		return 0, fmt.Errorf("%v is negative", float32(v))
	}
	s := strconv.FormatFloat(float64(v), 'f', -1, 32)
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, fmt.Errorf("%s is not a number", s)
	}
	r.Mul(r, big.NewRat(1e9, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, fmt.Errorf("%s is too precise or too large", s)
	}
	return r.Num().Int64(), nil
}
