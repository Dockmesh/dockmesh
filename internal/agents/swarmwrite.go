package agents

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
)

// Labels that mark Swarm objects Dockmesh created: services carry the hash
// of the spec Dockmesh wrote, networks the stack they were created for. The
// CLI's namespace label alone is no proof, `docker stack deploy` sets it
// too.
const (
	LabelSpecHash      = "dockmesh.spec-hash"
	LabelDockmeshStack = "dockmesh.stack"
)

// SwarmWriteWait is how long the server waits for a write frame's answer.
// The agent stops a minute earlier, so it reports what it did.
const SwarmWriteWait = 10 * time.Minute

// Bounds for one write frame.
const (
	MaxSwarmWriteServices = 200
	MaxSwarmWriteNetworks = 100
)

// Outcomes of one Swarm write.
const (
	SwarmActionCreated = "created"
	SwarmActionUpdated = "updated"
	SwarmActionExists  = "exists"
	SwarmActionRemoved = "removed"
	SwarmActionMissing = "missing"
	SwarmActionFailed  = "failed"
	// SwarmActionUnknown: the write was cut off; the daemon may have
	// applied it.
	SwarmActionUnknown = "unknown"
)

// SwarmDeployReq deploys one stack's networks and services through a
// manager, as `docker stack deploy` does: missing networks are created,
// services created or updated by name. The agent refuses the whole
// request before any write when a name already belongs to something that
// is not this stack's.
type SwarmDeployReq struct {
	Stack            string                  `json:"stack"`
	Networks         []network.CreateRequest `json:"networks,omitempty"`
	ExternalNetworks []string                `json:"external_networks,omitempty"`
	Services         []SwarmServiceWrite     `json:"services"`
}

// SwarmServiceWrite is one service spec with the registry credentials for
// its image. The manager pins the image to its digest.
type SwarmServiceWrite struct {
	Spec         swarm.ServiceSpec `json:"spec"`
	RegistryAuth string            `json:"registry_auth,omitempty"`
}

// SwarmWriteResult is what happened to one network or service.
type SwarmWriteResult struct {
	Name     string   `json:"name"`
	ID       string   `json:"id,omitempty"`
	Action   string   `json:"action"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// SwarmDeployResp lists every write in the order it was done. Error is set
// when the deploy stopped after a write failed; the writes before it stay
// applied.
type SwarmDeployResp struct {
	Networks []SwarmWriteResult `json:"networks"`
	Services []SwarmWriteResult `json:"services"`
	Error    string             `json:"error,omitempty"`
}

// SwarmRemoveReq removes services and then networks of one stack by name.
// Only objects carrying the stack's namespace label are removed.
type SwarmRemoveReq struct {
	Stack    string   `json:"stack"`
	Services []string `json:"services,omitempty"`
	Networks []string `json:"networks,omitempty"`
}

// SwarmRemoveResp lists the outcome per object; a name that no longer
// exists is "missing".
type SwarmRemoveResp struct {
	Services []SwarmWriteResult `json:"services"`
	Networks []SwarmWriteResult `json:"networks"`
}

// ErrSwarmWriteRefused prefixes every refusal of a write request.
var ErrSwarmWriteRefused = errors.New("swarm write refused")

var (
	swarmStackName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	swarmObjName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	specHashRe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSwarmWriteRefused, fmt.Sprintf(format, args...))
}

// ValidStackName reports whether s is a stack name the write frames accept.
func ValidStackName(s string) bool { return len(s) <= 63 && swarmStackName.MatchString(s) }

// Validate checks a deploy request against what the converter produces.
// The agent runs it before touching the daemon, the server before
// sending; nothing outside that shape is ever written.
func (r SwarmDeployReq) Validate() error {
	if !ValidStackName(r.Stack) {
		return refuse("invalid stack name")
	}
	if len(r.Services) == 0 || len(r.Services) > MaxSwarmWriteServices {
		return refuse("a stack deploy carries 1 to %d services", MaxSwarmWriteServices)
	}
	if len(r.Networks)+len(r.ExternalNetworks) > MaxSwarmWriteNetworks {
		return refuse("a stack deploy carries at most %d networks", MaxSwarmWriteNetworks)
	}
	known := map[string]bool{}
	for _, n := range r.Networks {
		if err := validNetwork(r.Stack, n); err != nil {
			return err
		}
		if known[n.Name] {
			return refuse("network %s listed twice", n.Name)
		}
		known[n.Name] = true
	}
	for _, n := range r.ExternalNetworks {
		if !swarmObjName.MatchString(n) {
			return refuse("invalid external network name")
		}
		if known[n] {
			return refuse("network %s listed twice", n)
		}
		known[n] = true
	}
	seen := map[string]bool{}
	for _, s := range r.Services {
		if err := validService(r.Stack, s.Spec, known); err != nil {
			return err
		}
		if seen[s.Spec.Name] {
			return refuse("service %s listed twice", s.Spec.Name)
		}
		seen[s.Spec.Name] = true
		if len(s.RegistryAuth) > 64<<10 {
			return refuse("registry auth of %s too long", s.Spec.Name)
		}
	}
	return nil
}

func validNetwork(stack string, n network.CreateRequest) error {
	if !swarmObjName.MatchString(n.Name) {
		return refuse("invalid network name")
	}
	if !strings.HasPrefix(n.Name, stack+"_") {
		return refuse("network %s: networks a stack creates are named %s_<network>", n.Name, stack)
	}
	if n.Labels[LabelStackNamespace] != stack || n.Labels[LabelDockmeshStack] != stack {
		return refuse("network %s lacks the stack labels", n.Name)
	}
	if n.Driver != "overlay" {
		return refuse("network %s: only overlay networks are created", n.Name)
	}
	if n.Scope != "" && n.Scope != "swarm" {
		return refuse("network %s: scope must be swarm", n.Name)
	}
	if n.Ingress || n.ConfigOnly || n.ConfigFrom != nil {
		return refuse("network %s: ingress and config networks are not created", n.Name)
	}
	return nil
}

// validService refuses what the converter never writes: other runtimes,
// privileges, added capabilities, sysctls, attachments to networks the
// request doesn't name.
func validService(stack string, spec swarm.ServiceSpec, networks map[string]bool) error {
	prefix := stack + "_"
	if !strings.HasPrefix(spec.Name, prefix) || !swarmObjName.MatchString(spec.Name) {
		return refuse("service name must start with %s", prefix)
	}
	if spec.Labels[LabelStackNamespace] != stack {
		return refuse("service %s lacks the stack label", spec.Name)
	}
	if !specHashRe.MatchString(spec.Labels[LabelSpecHash]) {
		return refuse("service %s lacks the spec hash", spec.Name)
	}
	t := spec.TaskTemplate
	if t.Runtime != "" && t.Runtime != swarm.RuntimeContainer || t.PluginSpec != nil || t.NetworkAttachmentSpec != nil {
		return refuse("service %s: only container services are deployed", spec.Name)
	}
	c := t.ContainerSpec
	if c == nil || c.Image == "" {
		return refuse("service %s has no image", spec.Name)
	}
	if c.Labels[LabelStackNamespace] != stack {
		return refuse("service %s: container labels lack the stack label", spec.Name)
	}
	if p := c.Privileges; p != nil && (p.CredentialSpec != nil || p.SELinuxContext != nil || p.Seccomp != nil || p.AppArmor != nil || p.NoNewPrivileges) {
		return refuse("service %s: privileges are not set by a stack deploy", spec.Name)
	}
	if len(c.CapabilityAdd) > 0 || len(c.CapabilityDrop) > 0 || len(c.Sysctls) > 0 || c.Init != nil || c.Isolation != "" {
		return refuse("service %s: container options are not set by a stack deploy", spec.Name)
	}
	for _, m := range c.Mounts {
		switch m.Type {
		case mount.TypeVolume, mount.TypeBind, mount.TypeTmpfs, mount.TypeImage:
		default:
			return refuse("service %s: mount type %s", spec.Name, m.Type)
		}
		if m.Type == mount.TypeBind && !strings.HasPrefix(m.Source, "/") {
			return refuse("service %s: bind sources must be absolute", spec.Name)
		}
	}
	for _, sr := range c.Secrets {
		if sr == nil || sr.SecretID != "" || !swarmObjName.MatchString(sr.SecretName) {
			return refuse("service %s: secrets are referenced by name only", spec.Name)
		}
	}
	for _, cr := range c.Configs {
		if cr == nil || cr.ConfigID != "" || cr.Runtime != nil || !swarmObjName.MatchString(cr.ConfigName) {
			return refuse("service %s: configs are referenced by name only", spec.Name)
		}
	}
	for _, a := range t.Networks {
		if !networks[a.Target] {
			return refuse("service %s attaches to a network the deploy doesn't name", spec.Name)
		}
	}
	return nil
}

// Validate checks a remove request: only the stack's own names.
func (r SwarmRemoveReq) Validate() error {
	if !ValidStackName(r.Stack) {
		return refuse("invalid stack name")
	}
	if len(r.Services) == 0 && len(r.Networks) == 0 {
		return refuse("nothing to remove")
	}
	if len(r.Services) > MaxSwarmWriteServices || len(r.Networks) > MaxSwarmWriteNetworks {
		return refuse("too many objects in one remove")
	}
	prefix := r.Stack + "_"
	for _, s := range r.Services {
		if !strings.HasPrefix(s, prefix) || !swarmObjName.MatchString(s) {
			return refuse("service name must start with %s", prefix)
		}
	}
	for _, n := range r.Networks {
		if !swarmObjName.MatchString(n) {
			return refuse("invalid network name")
		}
	}
	return nil
}
