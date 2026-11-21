// Package swarmconvert turns a compose project into the Swarm service and
// network specs of a stack, the way `docker stack deploy` does. The
// mapping follows docker/cli's cli/compose/convert (Apache-2.0, see
// NOTICE); the golden cases in internal/swarmgolden pin it to what the CLI
// sends.
//
// Every compose key the converter does not map is listed in
// Result.NotConverted, never dropped silently.
package swarmconvert

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
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

// stackName is the stacks manager's name rule. Names without `_` keep
// `<stack>_<key>` from one stack apart from another stack's names.
var stackName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

var errNoImage = errors.New("no image")

// Result is the converted stack.
type Result struct {
	Services []swarm.ServiceSpec
	// Networks are the networks the deploy creates; ExternalNetworks the
	// ones it has to find on the Swarm.
	Networks         []network.CreateRequest
	ExternalNetworks []string
	// NotConverted lists the compose keys that are set but not part of
	// the specs above, sorted by service and key.
	NotConverted []NotConverted
	// Warnings say where a converted spec behaves differently on a Swarm
	// than on one host, sorted by service and key.
	Warnings []Warning
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

// Warning is a converted key that behaves differently on a Swarm than on
// one host.
type Warning struct {
	Service string `json:"service,omitempty"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

func (w Warning) String() string {
	s := w.Key + ": " + w.Message
	if w.Service != "" {
		s = w.Service + ": " + s
	}
	return s
}

// Convert converts project into the specs of the stack named stack. The project is loaded with its
// environment resolved, as for a host deploy.
func Convert(project *types.Project, stack string) (*Result, error) {
	if !stackName.MatchString(stack) {
		return nil, fmt.Errorf("stack name %q is not lower-case letters, digits and dashes", stack)
	}
	res := &Result{}
	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := project.Services[name]
		spec, err := convertService(stack, svc, project)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		res.Services = append(res.Services, spec)
		left, err := notConverted(svc)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		res.NotConverted = append(res.NotConverted, left...)
		res.Warnings = append(res.Warnings, serviceWarnings(svc, project)...)
	}
	nets, external, err := convertNetworks(stack, project)
	if err != nil {
		return nil, err
	}
	res.Networks, res.ExternalNetworks = nets, external
	left, err := projectNotConverted(project)
	if err != nil {
		return nil, err
	}
	res.NotConverted = append(res.NotConverted, left...)
	res.Warnings = append(res.Warnings, networkWarnings(project)...)
	sort.SliceStable(res.NotConverted, func(i, j int) bool {
		a, b := res.NotConverted[i], res.NotConverted[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Key < b.Key
	})
	sort.SliceStable(res.Warnings, func(i, j int) bool {
		a, b := res.Warnings[i], res.Warnings[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Key < b.Key
	})
	return res, nil
}

func scoped(stack, name string) string { return stack + "_" + name }

func convertService(stack string, svc types.ServiceConfig, project *types.Project) (swarm.ServiceSpec, error) {
	if svc.Image == "" {
		return swarm.ServiceSpec{}, errNoImage
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
		return swarm.ServiceSpec{}, err
	}
	mode, err := convertMode(svc.Deploy)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	endpoint, err := convertEndpoint(svc.Ports)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	if endpoint.Mode, err = convertEndpointMode(svc.Deploy); err != nil {
		return swarm.ServiceSpec{}, err
	}
	restart, err := convertRestartPolicy(svc.Deploy)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	resources, err := convertResources(svc.Deploy)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	var update, rollback *swarm.UpdateConfig
	if svc.Deploy != nil {
		if update, err = convertUpdateConfig("update_config", svc.Deploy.UpdateConfig); err != nil {
			return swarm.ServiceSpec{}, err
		}
		if rollback, err = convertUpdateConfig("rollback_config", svc.Deploy.RollbackConfig); err != nil {
			return swarm.ServiceSpec{}, err
		}
	}
	mounts, err := convertMounts(stack, svc, project.Volumes)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	networks, err := convertServiceNetworks(stack, svc, project.Networks)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	secrets, err := convertSecrets(svc, project.Secrets)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	configs, err := convertConfigs(svc, project.Configs)
	if err != nil {
		return swarm.ServiceSpec{}, err
	}
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: scoped(stack, svc.Name), Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:       svc.Image,
				Labels:      containerLabels,
				Command:     []string(svc.Entrypoint),
				Args:        []string(svc.Command),
				Env:         convertEnvironment(svc.Environment),
				Healthcheck: healthcheck,
				Mounts:      mounts,
				Secrets:     secrets,
				Configs:     configs,
				Privileges:  &swarm.Privileges{},
			},
			Resources:     resources,
			RestartPolicy: restart,
			Placement:     convertPlacement(svc.Deploy),
			Networks:      networks,
		},
		Mode:           mode,
		UpdateConfig:   update,
		RollbackConfig: rollback,
		EndpointSpec:   endpoint,
	}, nil
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

func convertEndpoint(ports []types.ServicePortConfig) (*swarm.EndpointSpec, error) {
	out := &swarm.EndpointSpec{}
	for _, p := range ports {
		pc := swarm.PortConfig{
			Protocol:    network.IPProtocol(p.Protocol),
			TargetPort:  p.Target,
			PublishMode: swarm.PortConfigPublishMode(p.Mode),
		}
		if p.Published != "" {
			n, err := strconv.ParseUint(p.Published, 10, 16)
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

// serviceKeys says how the converter treats each key of a service in
// compose-go's JSON form: converted keys are mapped completely, walked
// keys are maps or lists of maps whose own keys are looked up one level
// down. Every other key that is set is reported as not converted.
var serviceKeys = keyTable{
	converted: set(
		"image", "environment", "env_file", "command", "entrypoint", "labels", "healthcheck",
		"secrets.source", "secrets.target", "secrets.uid", "secrets.gid", "secrets.mode",
		"configs.source", "configs.target", "configs.uid", "configs.gid", "configs.mode",
		"ports.mode", "ports.target", "ports.published", "ports.protocol",
		"deploy.replicas", "deploy.labels", "deploy.restart_policy", "deploy.mode", "deploy.endpoint_mode",
		"deploy.placement.constraints", "deploy.placement.preferences", "deploy.placement.max_replicas_per_node",
		"deploy.update_config.parallelism", "deploy.update_config.delay", "deploy.update_config.failure_action",
		"deploy.update_config.monitor", "deploy.update_config.max_failure_ratio", "deploy.update_config.order",
		"deploy.rollback_config.parallelism", "deploy.rollback_config.delay", "deploy.rollback_config.failure_action",
		"deploy.rollback_config.monitor", "deploy.rollback_config.max_failure_ratio", "deploy.rollback_config.order",
		"deploy.resources.limits.cpus", "deploy.resources.limits.memory", "deploy.resources.limits.pids",
		"deploy.resources.reservations.cpus", "deploy.resources.reservations.memory",
		"deploy.resources.reservations.generic_resources",
		"volumes.type", "volumes.source", "volumes.target", "volumes.read_only", "volumes.consistency",
		"volumes.bind.propagation", "volumes.volume.nocopy", "volumes.volume.subpath",
		"volumes.tmpfs.size", "volumes.image.subpath",
		"networks.aliases", "networks.driver_opts",
	),
	walked: set(
		"ports", "deploy", "deploy.placement", "deploy.update_config", "deploy.rollback_config",
		"deploy.resources", "deploy.resources.limits", "deploy.resources.reservations",
		"volumes", "volumes.bind", "volumes.volume", "volumes.tmpfs", "volumes.image",
		"secrets", "configs",
	),
	// Service networks are keyed by network name; the walk goes through
	// the names to the settings of each attachment.
	named: set("networks"),
}

var networkKeys = keyTable{
	converted: set("name", "driver", "driver_opts", "internal", "attachable", "labels", "external",
		"ipam.driver", "ipam.config.subnet"),
	walked: set("ipam", "ipam.config"),
}

var volumeKeys = keyTable{
	converted: set("name", "driver", "driver_opts", "labels", "external"),
}

// hints names the Swarm key for compose keys a Swarm ignores.
var hints = map[string]string{
	"restart": "deploy.restart_policy",
}

type keyTable struct {
	converted, walked, named map[string]bool
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// unmapped returns the dotted paths below v that t does not convert,
// sorted and without duplicates. v is compose-go's JSON form, so a key
// compose-go learns later shows up here instead of vanishing.
func (t keyTable) unmapped(v any) []string {
	seen := map[string]bool{}
	var walk func(path string, v any)
	walkMap := func(path string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			if !empty(v) {
				seen[path] = true
			}
			return
		}
		for k, el := range m {
			if path != "" {
				k = path + "." + k
			}
			walk(k, el)
		}
	}
	walk = func(path string, v any) {
		switch {
		case empty(v) || t.converted[path]:
		case t.named[path]:
			m, ok := v.(map[string]any)
			if !ok {
				seen[path] = true
				return
			}
			for _, el := range m {
				walkMap(path, el)
			}
		case t.walked[path]:
			if list, ok := v.([]any); ok {
				for _, el := range list {
					walkMap(path, el)
				}
				return
			}
			walkMap(path, v)
		default:
			seen[path] = true
		}
	}
	walkMap("", v)
	return sortedKeys(seen)
}

func jsonForm(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// notConverted lists the keys of svc that convertService leaves out.
func notConverted(svc types.ServiceConfig) ([]NotConverted, error) {
	v, err := jsonForm(svc)
	if err != nil {
		return nil, err
	}
	// The name is the map key of the service, not a setting.
	delete(v.(map[string]any), "name")
	keys := serviceKeys.unmapped(v)
	out := make([]NotConverted, 0, len(keys))
	for _, k := range keys {
		out = append(out, NotConverted{Service: svc.Name, Key: k, Hint: hints[k]})
	}
	return out, nil
}

// projectNotConverted lists the top-level keys besides services that the
// converter leaves out: settings of the used networks and volumes it
// doesn't map, networks no service uses, volumes no service mounts,
// secrets, configs, jobs and models.
func projectNotConverted(project *types.Project) ([]NotConverted, error) {
	usedNets := usedNetworks(project)
	usedVols := map[string]bool{}
	for _, svc := range project.Services {
		for _, v := range svc.Volumes {
			if v.Type == types.VolumeTypeVolume || v.Type == "" {
				usedVols[v.Source] = true
			}
		}
	}
	var out []NotConverted
	for _, name := range sortedKeys(project.Networks) {
		if !usedNets[name] {
			if name != defaultNetwork || !isPlainDefault(project.Networks[name]) {
				out = append(out, NotConverted{Key: "networks." + name})
			}
			continue
		}
		left, err := topLevelUnmapped(networkKeys, "networks."+name, project.Networks[name])
		if err != nil {
			return nil, err
		}
		out = append(out, left...)
	}
	for _, name := range sortedKeys(project.Volumes) {
		if !usedVols[name] {
			out = append(out, NotConverted{Key: "volumes." + name})
			continue
		}
		left, err := topLevelUnmapped(volumeKeys, "volumes."+name, project.Volumes[name])
		if err != nil {
			return nil, err
		}
		out = append(out, left...)
	}
	for _, name := range sortedKeys(project.Secrets) {
		if !project.Secrets[name].External {
			out = append(out, NotConverted{Key: "secrets." + name})
		}
	}
	for _, name := range sortedKeys(project.Configs) {
		if !project.Configs[name].External {
			out = append(out, NotConverted{Key: "configs." + name})
		}
	}
	for _, name := range sortedKeys(project.Jobs) {
		out = append(out, NotConverted{Key: "jobs." + name})
	}
	for _, name := range sortedKeys(project.Models) {
		out = append(out, NotConverted{Key: "models." + name})
	}
	return out, nil
}

func topLevelUnmapped(t keyTable, prefix string, v any) ([]NotConverted, error) {
	form, err := jsonForm(v)
	if err != nil {
		return nil, err
	}
	keys := t.unmapped(form)
	out := make([]NotConverted, 0, len(keys))
	for _, k := range keys {
		out = append(out, NotConverted{Key: prefix + "." + k})
	}
	return out, nil
}

// isPlainDefault reports whether the default network is the one compose-go
// adds by itself: only its name set.
func isPlainDefault(n types.NetworkConfig) bool {
	n.Name = ""
	return reflect.DeepEqual(n, types.NetworkConfig{})
}

// empty reports whether a value from compose-go's JSON form was left
// unset. compose-go omits unset fields, so a value that is present counts
// as set, even false or 0; only empty strings, maps and lists don't.
func empty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
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

// Strings returns the String form of each entry, for messages and tests.
func Strings(ns []NotConverted) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.String()
	}
	return out
}
