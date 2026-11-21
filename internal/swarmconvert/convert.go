// Package swarmconvert turns a compose project into the Swarm service and
// network specs of a stack, the way `docker stack deploy` does. The
// mapping follows docker/cli's cli/compose/convert (Apache-2.0, see
// NOTICE); the golden cases in internal/swarmgolden pin it to what the CLI
// sends.
//
// Every compose key the converter does not map yet is listed in
// Result.NotConverted, never dropped silently.
package swarmconvert

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
)

// Labels the CLI puts on everything it creates for a stack.
const (
	LabelNamespace = "com.docker.stack.namespace"
	LabelImage     = "com.docker.stack.image"
)

const defaultNetwork = "default"

// Result is the converted stack.
type Result struct {
	Services []swarm.ServiceSpec
	Networks []network.CreateRequest
	// NotConverted lists the compose keys that are set but not part of
	// the specs above, sorted by service and key.
	NotConverted []NotConverted
}

// NotConverted is one compose key the converter left out. Service is ""
// for top-level keys; Key is the compose path (`deploy.resources`).
type NotConverted struct {
	Service string `json:"service,omitempty"`
	Key     string `json:"key"`
	// Hint names the key that does this on a Swarm, when there is one.
	Hint string `json:"hint,omitempty"`
}

func (n NotConverted) String() string {
	s := n.Key
	if n.Service != "" {
		s = n.Service + ": " + s
	}
	if n.Hint != "" {
		s += " (use " + n.Hint + ")"
	}
	return s
}

// Convert converts project as stack stack. The project is loaded with its
// environment resolved, as for a host deploy.
func Convert(project *types.Project, stack string) (*Result, error) {
	if stack == "" {
		return nil, fmt.Errorf("no stack name")
	}
	res := &Result{}
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	usesDefault := false
	for _, name := range names {
		svc := project.Services[name]
		spec, def, err := convertService(stack, svc)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		usesDefault = usesDefault || def
		res.Services = append(res.Services, spec)
		left, err := notConverted(svc)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		res.NotConverted = append(res.NotConverted, left...)
	}
	if usesDefault {
		res.Networks = append(res.Networks, network.CreateRequest{
			Name:   scoped(stack, defaultNetwork),
			Driver: "overlay",
			Labels: map[string]string{LabelNamespace: stack},
		})
	}
	res.NotConverted = append(res.NotConverted, projectNotConverted(project)...)
	sort.SliceStable(res.NotConverted, func(i, j int) bool {
		a, b := res.NotConverted[i], res.NotConverted[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Key < b.Key
	})
	return res, nil
}

func scoped(stack, name string) string { return stack + "_" + name }

func convertService(stack string, svc types.ServiceConfig) (swarm.ServiceSpec, bool, error) {
	if svc.Image == "" {
		return swarm.ServiceSpec{}, false, fmt.Errorf("no image")
	}
	labels := map[string]string{}
	if svc.Deploy != nil {
		for k, v := range svc.Deploy.Labels {
			labels[k] = v
		}
	}
	labels[LabelImage] = svc.Image
	labels[LabelNamespace] = stack

	containerLabels := map[string]string{}
	for k, v := range svc.Labels {
		containerLabels[k] = v
	}
	containerLabels[LabelNamespace] = stack

	healthcheck, err := convertHealthcheck(svc.HealthCheck)
	if err != nil {
		return swarm.ServiceSpec{}, false, err
	}
	mode, err := convertMode(svc.Deploy)
	if err != nil {
		return swarm.ServiceSpec{}, false, err
	}
	endpoint, err := convertEndpoint(svc.Ports)
	if err != nil {
		return swarm.ServiceSpec{}, false, err
	}
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: scoped(stack, svc.Name), Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:       svc.Image,
				Labels:      containerLabels,
				Command:     []string(svc.Entrypoint),
				Args:        []string(svc.Command),
				Env:         convertEnvironment(svc.Environment),
				Healthcheck: healthcheck,
				Privileges:  &swarm.Privileges{},
			},
			RestartPolicy: convertRestartPolicy(svc.Deploy),
		},
		Mode:         mode,
		EndpointSpec: endpoint,
	}
	def := onlyDefaultNetwork(svc)
	if def {
		spec.TaskTemplate.Networks = []swarm.NetworkAttachmentConfig{{
			Target:  scoped(stack, defaultNetwork),
			Aliases: []string{svc.Name},
		}}
	}
	return spec, def, nil
}

// onlyDefaultNetwork reports whether svc is attached to the stack's
// default network alone, which compose-go adds to services without
// `networks:`. Any other attachment waits for the network conversion.
func onlyDefaultNetwork(svc types.ServiceConfig) bool {
	if len(svc.Networks) != 1 {
		return false
	}
	cfg, ok := svc.Networks[defaultNetwork]
	return ok && cfg == nil
}

func convertEnvironment(env types.MappingWithEquals) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		if v == nil {
			out = append(out, k)
			continue
		}
		out = append(out, k+"="+*v)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func convertHealthcheck(hc *types.HealthCheckConfig) (*container.HealthConfig, error) {
	if hc == nil {
		return nil, nil
	}
	if hc.Disable {
		if len(hc.Test) != 0 {
			return nil, fmt.Errorf("healthcheck: test and disable can't be set at the same time")
		}
		return &container.HealthConfig{Test: []string{"NONE"}}, nil
	}
	out := &container.HealthConfig{
		Test:          []string(hc.Test),
		Interval:      duration(hc.Interval),
		Timeout:       duration(hc.Timeout),
		StartPeriod:   duration(hc.StartPeriod),
		StartInterval: duration(hc.StartInterval),
	}
	if hc.Retries != nil {
		if *hc.Retries > math.MaxInt32 {
			return nil, fmt.Errorf("healthcheck: retries %d is too large", *hc.Retries)
		}
		out.Retries = int(*hc.Retries)
	}
	return out, nil
}

func duration(d *types.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return time.Duration(*d)
}

func durationPtr(d *types.Duration) *time.Duration {
	if d == nil {
		return nil
	}
	v := time.Duration(*d)
	return &v
}

func convertMode(deploy *types.DeployConfig) (swarm.ServiceMode, error) {
	mode := swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}}
	if deploy == nil || deploy.Replicas == nil {
		return mode, nil
	}
	if *deploy.Replicas < 0 {
		return mode, fmt.Errorf("deploy.replicas must not be negative")
	}
	n := uint64(*deploy.Replicas)
	mode.Replicated.Replicas = &n
	return mode, nil
}

func convertRestartPolicy(deploy *types.DeployConfig) *swarm.RestartPolicy {
	if deploy == nil || deploy.RestartPolicy == nil {
		return nil
	}
	p := deploy.RestartPolicy
	return &swarm.RestartPolicy{
		Condition:   swarm.RestartPolicyCondition(p.Condition),
		Delay:       durationPtr(p.Delay),
		MaxAttempts: p.MaxAttempts,
		Window:      durationPtr(p.Window),
	}
}

func convertEndpoint(ports []types.ServicePortConfig) (*swarm.EndpointSpec, error) {
	out := &swarm.EndpointSpec{}
	for _, p := range ports {
		pc := swarm.PortConfig{
			Protocol:    network.IPProtocol(p.Protocol),
			TargetPort:  p.Target,
			PublishMode: swarm.PortConfigPublishMode(p.Mode),
		}
		if p.Published != "" {
			n, err := strconv.ParseUint(p.Published, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("ports: published port %q is not a single port", p.Published)
			}
			pc.PublishedPort = uint32(n)
		}
		out.Ports = append(out.Ports, pc)
	}
	sort.SliceStable(out.Ports, func(i, j int) bool { return out.Ports[i].PublishedPort < out.Ports[j].PublishedPort })
	return out, nil
}

// converted lists the compose keys convertService maps. Keys are the JSON
// names compose-go writes; nested keys are dotted.
var converted = map[string]bool{
	"image":                  true,
	"environment":            true,
	"command":                true,
	"entrypoint":             true,
	"labels":                 true,
	"healthcheck":            true,
	"ports.mode":             true,
	"ports.target":           true,
	"ports.published":        true,
	"ports.protocol":         true,
	"deploy.replicas":        true,
	"deploy.labels":          true,
	"deploy.restart_policy":  true,
	"deploy.mode=replicated": true,
}

// hints names the Swarm key for compose keys a Swarm ignores.
var hints = map[string]string{
	"restart": "deploy.restart_policy",
}

// notConverted lists the keys of svc that convertService leaves out. It
// walks compose-go's JSON form, so a key compose-go learns later shows up
// here instead of vanishing.
func notConverted(svc types.ServiceConfig) ([]NotConverted, error) {
	raw, err := json.Marshal(svc)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	var out []NotConverted
	add := func(key string) {
		out = append(out, NotConverted{Service: svc.Name, Key: key, Hint: hints[key]})
	}
	for _, k := range sortedKeys(m) {
		v := m[k]
		if empty(v) {
			continue
		}
		switch k {
		case "networks":
			if !onlyDefaultNetwork(svc) {
				add(k)
			}
		case "ports":
			seen := map[string]bool{}
			for _, p := range v.([]any) {
				for pk, pv := range p.(map[string]any) {
					if !empty(pv) && !converted["ports."+pk] && !seen[pk] {
						seen[pk] = true
						add("ports." + pk)
					}
				}
			}
		case "deploy":
			for _, dk := range sortedKeys(v.(map[string]any)) {
				dv := v.(map[string]any)[dk]
				switch {
				case empty(dv):
				case dk == "mode":
					if dv != "replicated" {
						add("deploy.mode")
					}
				case !converted["deploy."+dk]:
					add("deploy." + dk)
				}
			}
		default:
			if !converted[k] {
				add(k)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// projectNotConverted lists the top-level keys besides services that the
// converter leaves out: every network but the default one, volumes,
// secrets and configs.
func projectNotConverted(project *types.Project) []NotConverted {
	var out []NotConverted
	for _, name := range sortedKeys(project.Networks) {
		if name == defaultNetwork && isPlainDefault(project.Networks[name]) {
			continue
		}
		out = append(out, NotConverted{Key: "networks." + name})
	}
	for _, name := range sortedKeys(project.Volumes) {
		out = append(out, NotConverted{Key: "volumes." + name})
	}
	for _, name := range sortedKeys(project.Secrets) {
		out = append(out, NotConverted{Key: "secrets." + name})
	}
	for _, name := range sortedKeys(project.Configs) {
		out = append(out, NotConverted{Key: "configs." + name})
	}
	return out
}

// isPlainDefault reports whether the default network is the one compose-go
// adds by itself: only its name set.
func isPlainDefault(n types.NetworkConfig) bool {
	n.Name = ""
	raw, err := json.Marshal(n)
	if err != nil {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	return empty(m)
}

func empty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case map[string]any:
		// A key with a null value was written (`networks: {back:}`);
		// only keys holding empty values count as unset.
		for _, el := range t {
			if el == nil || !empty(el) {
				return false
			}
		}
		return true
	case []any:
		for _, el := range t {
			if !empty(el) {
				return false
			}
		}
		return true
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Strings formats ns one per line, for messages and tests.
func Strings(ns []NotConverted) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.String()
	}
	return out
}
