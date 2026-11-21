package swarmconvert

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// Plan is what a deploy of a stack to a Swarm would do: the converted
// specs, what it refuses and what it warns about. A plan with refusals
// must not be deployed.
type Plan struct {
	// Result is nil when the file could not be converted at all.
	Result   *Result
	Refusals []Finding
	Warnings []Finding
}

// Finding is one refusal or warning. Service is "" for top-level keys.
type Finding struct {
	Service string `json:"service,omitempty"`
	Key     string `json:"key"`
	Message string `json:"message"`
	// Hint names the key that does this on a Swarm, when there is one.
	Hint string `json:"hint,omitempty"`
}

func (f Finding) String() string {
	s := f.Key + ": " + f.Message
	if f.Service != "" {
		s = f.Service + ": " + s
	}
	if f.Hint != "" {
		s += " (use " + f.Hint + ")"
	}
	return s
}

// Rule is how Plan treats a compose key the converter does not map.
// Keys are compose paths; top-level names are `*` (`secrets.*`), and a
// few keys stand for a value check rather than a path
// (`build without image`, `depends_on.condition`, `volumes relative bind`).
type Rule struct {
	Key     string
	Refuse  bool
	Message string
	Hint    string
}

// Rules is the refuse list: every key here is refused or warned about
// with its reason. A key the converter leaves out that is not listed is
// refused, so a key nobody looked at can never be dropped silently.
var Rules = []Rule{
	{Key: "build without image", Refuse: true, Message: "A Swarm pulls every image from a registry. Build and push the image, then set image:."},
	{Key: "privileged", Refuse: true, Message: "Swarm services can't run privileged."},
	{Key: "devices", Refuse: true, Message: "Swarm services can't use host devices."},
	{Key: "gpus", Refuse: true, Message: "Swarm services can't request GPUs."},
	{Key: "deploy.resources.reservations.devices", Refuse: true, Message: "Swarm services can't reserve devices or GPUs."},
	{Key: "network_mode", Refuse: true, Message: "Swarm services can't use the network stack of the host or another container. Attach the service to networks instead."},
	{Key: "pid", Refuse: true, Message: "Swarm services can't share the process namespace of the host or another container."},
	{Key: "ipc", Refuse: true, Message: "Swarm services can't share the IPC namespace of the host or another container."},
	{Key: "volumes_from", Refuse: true, Message: "Swarm tasks can't use another container's volumes. Mount the same named volume instead."},
	{Key: "links", Refuse: true, Message: "Links don't exist on a Swarm; services reach each other by service name or a network alias.", Hint: "networks.<name>.aliases"},
	{Key: "external_links", Refuse: true, Message: "Links don't exist on a Swarm; services reach each other by service name or a network alias.", Hint: "networks.<name>.aliases"},
	{Key: "depends_on.condition", Refuse: true, Message: "Swarm starts services in no particular order and never waits for a dependency to be healthy or finished. Let the service retry until its dependency is up."},
	{Key: "ports.host_ip", Refuse: true, Message: "Swarm publishes a port on every address of every node, so the port would be reachable more widely than the file says."},
	{Key: "tmpfs", Refuse: true, Message: "Swarm ignores tmpfs:, so the data would land in the container's own filesystem.", Hint: "volumes with type: tmpfs"},
	{Key: "volumes.tmpfs.mode", Refuse: true, Message: "Swarm can't set the mode of a tmpfs mount."},
	{Key: "volumes relative bind", Refuse: true, Message: "Relative bind mounts have no fixed place in a Swarm. Use a named volume, or an absolute path with a placement constraint."},
	{Key: "networks.ipv4_address", Refuse: true, Message: "Swarm assigns task addresses itself; a fixed address can't be kept."},
	{Key: "networks.ipv6_address", Refuse: true, Message: "Swarm assigns task addresses itself; a fixed address can't be kept."},
	{Key: "networks.mac_address", Refuse: true, Message: "Swarm assigns task MAC addresses itself; a fixed address can't be kept."},
	{Key: "networks host", Refuse: true, Message: "The host, bridge and none networks are local to each node; Swarm services can't attach to them. Attach the service to an overlay network instead."},
	{Key: "secrets.*", Refuse: true, Message: "Secrets and configs owned by a Swarm stack are not supported yet. Use an external secret or config."},
	{Key: "configs.*", Refuse: true, Message: "Secrets and configs owned by a Swarm stack are not supported yet. Use an external secret or config."},
	{Key: "jobs.*", Refuse: true, Message: "Jobs are not deployed to a Swarm.", Hint: "deploy.mode: replicated-job"},
	{Key: "models.*", Refuse: true, Message: "Models can't run on a Swarm."},

	{Key: "restart", Message: "Swarm ignores restart:; tasks restart by deploy.restart_policy, which restarts them always by default.", Hint: "deploy.restart_policy"},
	{Key: "build", Message: "build is ignored; every node pulls the image from its registry."},
	{Key: "container_name", Message: "Swarm names tasks itself; container_name is ignored."},
	{Key: "depends_on", Message: "Swarm starts services in no particular order; depends_on is ignored."},
	{Key: "expose", Message: "expose is ignored; services on a shared network reach every port."},
	{Key: "scale", Message: "scale is ignored on a Swarm.", Hint: "deploy.replicas"},
	{Key: "pull_policy", Message: "pull_policy is ignored; each node pulls the image when a task needs it."},
	{Key: "ports.name", Message: "Port names are ignored on a Swarm."},
	{Key: "ports.app_protocol", Message: "app_protocol is ignored on a Swarm."},
	{Key: "volumes.bind.selinux", Message: "Swarm does not relabel bind mounts for SELinux; on SELinux nodes the task may not be able to read the path."},
	{Key: "networks.priority", Message: "Network priority is ignored on a Swarm."},
	{Key: "networks.gw_priority", Message: "Gateway priority is ignored on a Swarm."},
	{Key: "networks.interface_name", Message: "Interface names are ignored on a Swarm."},
	{Key: "networks.link_local_ips", Message: "Link-local addresses are ignored on a Swarm."},
	{Key: "networks.*", Message: "No service uses this network, so it is not created."},
	{Key: "networks other name", Message: "The network's name: is outside this stack's names, so the stack may share or take over a network another stack or a person created."},
	{Key: "volumes other name", Message: "The volume's name: is outside this stack's names, so tasks may mount data another stack or a person created."},
	{Key: "volumes.*", Message: "No service mounts this volume, so it is not created."},
}

var rulesByKey = func() map[string]Rule {
	m := make(map[string]Rule, len(Rules))
	for _, r := range Rules {
		m[r.Key] = r
	}
	return m
}()

// ruleKey is the Rules key of a not-converted entry: the name segment of
// a top-level key becomes `*`.
func ruleKey(n NotConverted) string {
	if n.Service != "" {
		return n.Key
	}
	parts := strings.SplitN(n.Key, ".", 3)
	if len(parts) < 2 {
		return n.Key
	}
	parts[1] = "*"
	return strings.Join(parts, ".")
}

// finding applies the rule for key to one entry; an unlisted key is
// refused.
func finding(service, key, ruleK string) (Finding, bool) {
	r, ok := rulesByKey[ruleK]
	if !ok {
		return Finding{Service: service, Key: key,
			Message: "Dockmesh does not convert " + key + " for a Swarm, so a deploy would drop it."}, true
	}
	return Finding{Service: service, Key: key, Message: r.Message, Hint: r.Hint}, r.Refuse
}

// NewPlan converts project for stack and sorts every key it leaves out
// into refusals and warnings. unresolved is the same file loaded without
// resolving relative paths, which project has made absolute; with nil,
// relative binds are not detected.
func NewPlan(project, unresolved *types.Project, stack string) *Plan {
	p := &Plan{}
	add := func(f Finding, refuse bool) {
		if refuse {
			p.Refusals = append(p.Refusals, f)
		} else {
			p.Warnings = append(p.Warnings, f)
		}
	}
	noImage := false
	for _, name := range sortedKeys(project.Services) {
		svc := project.Services[name]
		if svc.Build != nil && svc.Image == "" {
			noImage = true
			add(finding(name, "build", "build without image"))
		}
	}
	if unresolved != nil {
		for _, key := range sortedKeys(unresolved.Volumes) {
			if dev, ok := bindDevice(unresolved.Volumes[key]); ok && !filepath.IsAbs(dev) {
				add(finding("", "volumes."+key, "volumes relative bind"))
			}
		}
		for _, name := range sortedKeys(unresolved.Services) {
			for _, v := range unresolved.Services[name].Volumes {
				if v.Type == types.VolumeTypeBind && !filepath.IsAbs(v.Source) {
					add(finding(name, "volumes "+v.Target, "volumes relative bind"))
				}
			}
		}
	}
	res, err := Convert(project, stack)
	if err != nil {
		if !noImage || !errors.Is(err, errNoImage) {
			p.Refusals = append(p.Refusals, Finding{Key: "compose.yaml", Message: err.Error()})
		}
		p.sort()
		return p
	}
	p.Result = res
	for _, n := range res.NotConverted {
		key := ruleKey(n)
		if n.Service != "" && n.Key == "depends_on" && waitsForCondition(project.Services[n.Service]) {
			key = "depends_on.condition"
		}
		if n.Service != "" && n.Key == "build" && project.Services[n.Service].Image == "" {
			continue
		}
		add(finding(n.Service, n.Key, key))
	}
	foreignNames(project, stack, add)
	for _, w := range res.Warnings {
		p.Warnings = append(p.Warnings, Finding{Service: w.Service, Key: w.Key, Message: w.Message})
	}
	p.sort()
	return p
}

// foreignNames finds networks and volumes whose `name:` reaches outside
// the stack: the node-local host and bridge networks, which a service
// can't join, and names without the stack's prefix.
func foreignNames(project *types.Project, stack string, add func(Finding, bool)) {
	used := usedNetworks(project)
	for _, key := range sortedKeys(project.Networks) {
		nw := project.Networks[key]
		if !used[key] {
			continue
		}
		name := networkName(stack, key, nw)
		switch {
		case name == "host" || name == "bridge" || name == "none":
			add(finding("", "networks."+key, "networks host"))
		case !bool(nw.External) && !strings.HasPrefix(name, stack+"_"):
			add(finding("", "networks."+key, "networks other name"))
		}
	}
	for _, key := range sortedKeys(project.Volumes) {
		vol := project.Volumes[key]
		if !bool(vol.External) && vol.Name != "" && !strings.HasPrefix(vol.Name, stack+"_") {
			add(finding("", "volumes."+key, "volumes other name"))
		}
	}
}

// bindDevice returns the device of a local volume whose driver options
// make it a bind mount of a host path.
func bindDevice(v types.VolumeConfig) (string, bool) {
	if v.External || (v.Driver != "" && v.Driver != "local") {
		return "", false
	}
	for _, o := range strings.Split(v.DriverOpts["o"], ",") {
		if o == "bind" || o == "rbind" {
			return v.DriverOpts["device"], true
		}
	}
	return "", false
}

// waitsForCondition reports whether svc waits for a dependency to be
// healthy or finished, which a Swarm can't do.
func waitsForCondition(svc types.ServiceConfig) bool {
	for _, d := range svc.DependsOn {
		if d.Condition != "" && d.Condition != types.ServiceConditionStarted {
			return true
		}
	}
	return false
}

func (p *Plan) sort() {
	for _, fs := range [][]Finding{p.Refusals, p.Warnings} {
		sort.SliceStable(fs, func(i, j int) bool {
			if fs[i].Service != fs[j].Service {
				return fs[i].Service < fs[j].Service
			}
			return fs[i].Key < fs[j].Key
		})
	}
}
