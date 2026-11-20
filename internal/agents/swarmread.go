package agents

import (
	"errors"
	"net/netip"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
)

// ErrNotSwarmManager is what an agent answers to a Swarm read frame when
// its daemon is not a manager of an active Swarm.
var ErrNotSwarmManager = errors.New("this node is not a Swarm manager")

// ErrSwarmServiceNotFound answers a service inspect of an unknown service.
var ErrSwarmServiceNotFound = errors.New("swarm service not found")

// Bounds for what one manager may send and what the server accepts. They
// keep one answer far below MaxFrameBytes.
const (
	maxSwarmServices = 1000
	maxSwarmTasks    = 5000
	maxSwarmNetworks = 500
	maxListItems     = 64
	maxEnvKeys       = 256
	maxLabels        = 64
	maxTextLen       = 512
	maxLabelValueLen = 1024
)

// SwarmPort is a port a service publishes.
type SwarmPort struct {
	Protocol      string `json:"protocol,omitempty"`
	TargetPort    uint32 `json:"target_port"`
	PublishedPort uint32 `json:"published_port,omitempty"`
	PublishMode   string `json:"publish_mode,omitempty"`
}

// SwarmService is one entry of a manager's service list.
type SwarmService struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Image        string      `json:"image,omitempty"`
	Mode         string      `json:"mode"` // replicated | global | replicated-job | global-job
	Replicas     *uint64     `json:"replicas,omitempty"`
	RunningTasks uint64      `json:"running_tasks"`
	DesiredTasks uint64      `json:"desired_tasks"`
	Stack        string      `json:"stack,omitempty"`
	Ports        []SwarmPort `json:"ports,omitempty"`
	UpdateState  string      `json:"update_state,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// SwarmRef names a secret or config a service uses. Their data is never
// part of a service spec and never sent.
type SwarmRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Target string `json:"target,omitempty"`
}

type SwarmServiceNetwork struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"`
}

type SwarmMount struct {
	Type     string `json:"type"`
	Source   string `json:"source,omitempty"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type SwarmUpdatePolicy struct {
	Parallelism   uint64 `json:"parallelism"`
	DelayNS       int64  `json:"delay_ns,omitempty"`
	FailureAction string `json:"failure_action,omitempty"`
	Order         string `json:"order,omitempty"`
}

// SwarmServiceDetail is a service inspect without values that could be
// secret: env comes as variable names only, secrets and configs as
// references, and command, args and driver options are left out.
type SwarmServiceDetail struct {
	SwarmService
	Labels         map[string]string     `json:"labels,omitempty"`
	EnvKeys        []string              `json:"env_keys,omitempty"`
	Secrets        []SwarmRef            `json:"secrets,omitempty"`
	Configs        []SwarmRef            `json:"configs,omitempty"`
	Networks       []SwarmServiceNetwork `json:"networks,omitempty"`
	Mounts         []SwarmMount          `json:"mounts,omitempty"`
	Constraints    []string              `json:"constraints,omitempty"`
	MaxPerNode     uint64                `json:"max_replicas_per_node,omitempty"`
	LimitNanoCPUs  int64                 `json:"limit_nano_cpus,omitempty"`
	LimitMemory    int64                 `json:"limit_memory_bytes,omitempty"`
	UpdateConfig   *SwarmUpdatePolicy    `json:"update_config,omitempty"`
	RollbackConfig *SwarmUpdatePolicy    `json:"rollback_config,omitempty"`
	UpdateMessage  string                `json:"update_message,omitempty"`
}

// SwarmTask is one task of a service.
type SwarmTask struct {
	ID           string    `json:"id"`
	ServiceID    string    `json:"service_id"`
	NodeID       string    `json:"node_id,omitempty"`
	Slot         int       `json:"slot,omitempty"`
	DesiredState string    `json:"desired_state"`
	State        string    `json:"state"`
	Message      string    `json:"message,omitempty"`
	Error        string    `json:"error,omitempty"`
	ContainerID  string    `json:"container_id,omitempty"`
	Image        string    `json:"image,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SwarmNetwork is a network with Swarm scope (overlay, ingress).
type SwarmNetwork struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Driver     string    `json:"driver"`
	Attachable bool      `json:"attachable,omitempty"`
	Ingress    bool      `json:"ingress,omitempty"`
	Internal   bool      `json:"internal,omitempty"`
	Encrypted  bool      `json:"encrypted,omitempty"`
	Subnets    []string  `json:"subnets,omitempty"`
	Stack      string    `json:"stack,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// SwarmTasksReq filters the task list to one service when ServiceID is set.
type SwarmTasksReq struct {
	ServiceID string `json:"service_id,omitempty"`
}

// The answers carry Truncated when the manager had more than the bound.
type SwarmServicesResp struct {
	Services  []SwarmService `json:"services"`
	Truncated bool           `json:"truncated,omitempty"`
}

type SwarmTasksResp struct {
	Tasks     []SwarmTask `json:"tasks"`
	Truncated bool        `json:"truncated,omitempty"`
}

type SwarmNetworksResp struct {
	Networks  []SwarmNetwork `json:"networks"`
	Truncated bool           `json:"truncated,omitempty"`
}

const labelStackNamespace = "com.docker.stack.namespace"

// SwarmServicesFrom converts a daemon's service list (with status).
func SwarmServicesFrom(list []swarm.Service) SwarmServicesResp {
	out := make([]SwarmService, 0, len(list))
	for _, s := range list {
		out = append(out, serviceSummary(s))
	}
	return SanitizeSwarmServices(SwarmServicesResp{Services: out})
}

func serviceSummary(s swarm.Service) SwarmService {
	sv := SwarmService{
		ID:        s.ID,
		Name:      s.Spec.Name,
		Stack:     s.Spec.Labels[labelStackNamespace],
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
	if cs := s.Spec.TaskTemplate.ContainerSpec; cs != nil {
		sv.Image = cs.Image
	}
	switch m := s.Spec.Mode; {
	case m.Replicated != nil:
		sv.Mode = "replicated"
		sv.Replicas = m.Replicated.Replicas
	case m.Global != nil:
		sv.Mode = "global"
	case m.ReplicatedJob != nil:
		sv.Mode = "replicated-job"
	case m.GlobalJob != nil:
		sv.Mode = "global-job"
	}
	if s.ServiceStatus != nil {
		sv.RunningTasks = s.ServiceStatus.RunningTasks
		sv.DesiredTasks = s.ServiceStatus.DesiredTasks
	}
	ports := s.Endpoint.Ports
	if len(ports) == 0 && s.Spec.EndpointSpec != nil {
		ports = s.Spec.EndpointSpec.Ports
	}
	for _, p := range ports {
		sv.Ports = append(sv.Ports, SwarmPort{
			Protocol: string(p.Protocol), TargetPort: p.TargetPort,
			PublishedPort: p.PublishedPort, PublishMode: string(p.PublishMode),
		})
	}
	if s.UpdateStatus != nil {
		sv.UpdateState = string(s.UpdateStatus.State)
	}
	return sv
}

// SwarmServiceDetailFrom converts a daemon's service inspect.
func SwarmServiceDetailFrom(s swarm.Service) SwarmServiceDetail {
	d := SwarmServiceDetail{SwarmService: serviceSummary(s), Labels: s.Spec.Labels}
	t := s.Spec.TaskTemplate
	if cs := t.ContainerSpec; cs != nil {
		d.EnvKeys = envKeys(cs.Env)
		for _, r := range cs.Secrets {
			if r == nil {
				continue
			}
			ref := SwarmRef{ID: r.SecretID, Name: r.SecretName}
			if r.File != nil {
				ref.Target = r.File.Name
			}
			d.Secrets = append(d.Secrets, ref)
		}
		for _, r := range cs.Configs {
			if r == nil {
				continue
			}
			ref := SwarmRef{ID: r.ConfigID, Name: r.ConfigName}
			if r.File != nil {
				ref.Target = r.File.Name
			}
			d.Configs = append(d.Configs, ref)
		}
		for _, m := range cs.Mounts {
			d.Mounts = append(d.Mounts, SwarmMount{Type: string(m.Type), Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
		}
	}
	for _, n := range t.Networks {
		d.Networks = append(d.Networks, SwarmServiceNetwork{ID: n.Target, Aliases: n.Aliases})
	}
	if p := t.Placement; p != nil {
		d.Constraints = p.Constraints
		d.MaxPerNode = p.MaxReplicas
	}
	if r := t.Resources; r != nil && r.Limits != nil {
		d.LimitNanoCPUs = r.Limits.NanoCPUs
		d.LimitMemory = r.Limits.MemoryBytes
	}
	d.UpdateConfig = updatePolicy(s.Spec.UpdateConfig)
	d.RollbackConfig = updatePolicy(s.Spec.RollbackConfig)
	if s.UpdateStatus != nil {
		d.UpdateMessage = s.UpdateStatus.Message
	}
	return SanitizeSwarmServiceDetail(d)
}

func updatePolicy(u *swarm.UpdateConfig) *SwarmUpdatePolicy {
	if u == nil {
		return nil
	}
	return &SwarmUpdatePolicy{
		Parallelism: u.Parallelism, DelayNS: int64(u.Delay),
		FailureAction: string(u.FailureAction), Order: string(u.Order),
	}
}

// envKeys returns the variable names of KEY=VALUE entries.
func envKeys(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		out = append(out, k)
	}
	return out
}

// SwarmTasksFrom converts a daemon's task list, newest first.
func SwarmTasksFrom(list []swarm.Task) SwarmTasksResp {
	out := make([]SwarmTask, 0, len(list))
	for _, t := range list {
		st := SwarmTask{
			ID: t.ID, ServiceID: t.ServiceID, NodeID: t.NodeID, Slot: t.Slot,
			DesiredState: string(t.DesiredState), State: string(t.Status.State),
			Message: t.Status.Message, Error: t.Status.Err, UpdatedAt: t.Status.Timestamp,
		}
		if t.Status.ContainerStatus != nil {
			st.ContainerID = t.Status.ContainerStatus.ContainerID
		}
		if cs := t.Spec.ContainerSpec; cs != nil {
			st.Image = cs.Image
		}
		out = append(out, st)
	}
	return SanitizeSwarmTasks(SwarmTasksResp{Tasks: out})
}

// SwarmNetworksFrom keeps the networks with Swarm scope of a daemon's list.
func SwarmNetworksFrom(list []network.Summary) SwarmNetworksResp {
	out := make([]SwarmNetwork, 0, len(list))
	for _, n := range list {
		if n.Scope != "swarm" {
			continue
		}
		sn := SwarmNetwork{
			ID: n.ID, Name: n.Name, Driver: n.Driver, Attachable: n.Attachable,
			Ingress: n.Ingress, Internal: n.Internal, Stack: n.Labels[labelStackNamespace],
			CreatedAt: n.Created,
		}
		if _, ok := n.Options["encrypted"]; ok {
			sn.Encrypted = true
		}
		for _, c := range n.IPAM.Config {
			if c.Subnet.IsValid() {
				sn.Subnets = append(sn.Subnets, c.Subnet.String())
			}
		}
		out = append(out, sn)
	}
	return SanitizeSwarmNetworks(SwarmNetworksResp{Networks: out})
}

// The Sanitize functions run on both sides: on the agent before sending,
// and on the server on what arrived, since the answer comes from the
// agent host and is not trusted beyond that.

func SanitizeSwarmServices(r SwarmServicesResp) SwarmServicesResp {
	out := SwarmServicesResp{Services: make([]SwarmService, 0, min(len(r.Services), maxSwarmServices)), Truncated: r.Truncated}
	seen := map[string]bool{}
	for _, s := range r.Services {
		if len(out.Services) == maxSwarmServices {
			out.Truncated = true
			break
		}
		s, ok := sanitizeService(s)
		if !ok || seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out.Services = append(out.Services, s)
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Name < out.Services[j].Name })
	return out
}

func sanitizeService(s SwarmService) (SwarmService, bool) {
	s.ID = token(s.ID, 64, "")
	s.Name = text(s.Name, 128)
	if s.ID == "" || s.Name == "" {
		return SwarmService{}, false
	}
	switch s.Mode {
	case "replicated", "global", "replicated-job", "global-job":
	default:
		s.Mode = ""
	}
	if s.Mode != "replicated" {
		s.Replicas = nil
	}
	s.Image = text(s.Image, maxTextLen)
	s.Stack = text(s.Stack, 128)
	s.UpdateState = token(s.UpdateState, 32, "_")
	if len(s.Ports) > maxListItems {
		s.Ports = s.Ports[:maxListItems]
	}
	for i := range s.Ports {
		p := &s.Ports[i]
		p.Protocol = token(p.Protocol, 8, "")
		p.PublishMode = token(p.PublishMode, 16, "")
	}
	return s, true
}

func SanitizeSwarmServiceDetail(d SwarmServiceDetail) SwarmServiceDetail {
	s, ok := sanitizeService(d.SwarmService)
	if !ok {
		return SwarmServiceDetail{}
	}
	d.SwarmService = s
	d.Labels = labels(d.Labels)
	keys := make([]string, 0, min(len(d.EnvKeys), maxEnvKeys))
	for _, k := range d.EnvKeys {
		if len(keys) == maxEnvKeys {
			break
		}
		// An entry that still carries "=" came from a sender that didn't
		// strip the value; keep the name only.
		k, _, _ = strings.Cut(k, "=")
		if k = text(k, 256); k != "" {
			keys = append(keys, k)
		}
	}
	d.EnvKeys = keys
	d.Secrets = refs(d.Secrets)
	d.Configs = refs(d.Configs)
	d.Networks = capList(d.Networks)
	for i := range d.Networks {
		n := &d.Networks[i]
		n.ID = token(n.ID, 64, "")
		n.Aliases = texts(n.Aliases, 128)
	}
	d.Mounts = capList(d.Mounts)
	for i := range d.Mounts {
		m := &d.Mounts[i]
		m.Type = token(m.Type, 16, "")
		m.Source = text(m.Source, maxTextLen)
		m.Target = text(m.Target, maxTextLen)
	}
	d.Constraints = texts(d.Constraints, 256)
	d.UpdateConfig = sanitizePolicy(d.UpdateConfig)
	d.RollbackConfig = sanitizePolicy(d.RollbackConfig)
	d.UpdateMessage = text(d.UpdateMessage, maxTextLen)
	return d
}

func sanitizePolicy(p *SwarmUpdatePolicy) *SwarmUpdatePolicy {
	if p == nil {
		return nil
	}
	p.FailureAction = token(p.FailureAction, 16, "")
	p.Order = token(p.Order, 16, "-")
	return p
}

func refs(in []SwarmRef) []SwarmRef {
	in = capList(in)
	out := make([]SwarmRef, 0, len(in))
	for _, r := range in {
		out = append(out, SwarmRef{ID: token(r.ID, 64, ""), Name: text(r.Name, 128), Target: text(r.Target, maxTextLen)})
	}
	return out
}

func SanitizeSwarmTasks(r SwarmTasksResp) SwarmTasksResp {
	tasks := r.Tasks
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt) })
	out := SwarmTasksResp{Tasks: make([]SwarmTask, 0, min(len(tasks), maxSwarmTasks)), Truncated: r.Truncated}
	seen := map[string]bool{}
	for _, t := range tasks {
		if len(out.Tasks) == maxSwarmTasks {
			out.Truncated = true
			break
		}
		t.ID = token(t.ID, 64, "")
		t.ServiceID = token(t.ServiceID, 64, "")
		if t.ID == "" || t.ServiceID == "" || seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		t.NodeID = token(t.NodeID, 64, "")
		t.DesiredState = token(t.DesiredState, 16, "")
		t.State = token(t.State, 16, "")
		t.Message = text(t.Message, maxTextLen)
		t.Error = text(t.Error, maxTextLen)
		t.ContainerID = token(t.ContainerID, 64, "")
		t.Image = text(t.Image, maxTextLen)
		out.Tasks = append(out.Tasks, t)
	}
	return out
}

func SanitizeSwarmNetworks(r SwarmNetworksResp) SwarmNetworksResp {
	out := SwarmNetworksResp{Networks: make([]SwarmNetwork, 0, min(len(r.Networks), maxSwarmNetworks)), Truncated: r.Truncated}
	seen := map[string]bool{}
	for _, n := range r.Networks {
		if len(out.Networks) == maxSwarmNetworks {
			out.Truncated = true
			break
		}
		n.ID = token(n.ID, 64, "")
		n.Name = text(n.Name, 128)
		if n.ID == "" || n.Name == "" || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		n.Driver = text(n.Driver, 64)
		n.Stack = text(n.Stack, 128)
		subnets := make([]string, 0, len(n.Subnets))
		for _, s := range capList(n.Subnets) {
			if p, err := netip.ParsePrefix(s); err == nil {
				subnets = append(subnets, p.String())
			}
		}
		n.Subnets = subnets
		out.Networks = append(out.Networks, n)
	}
	sort.Slice(out.Networks, func(i, j int) bool { return out.Networks[i].Name < out.Networks[j].Name })
	return out
}

// text returns s cut to at most max bytes on a rune boundary, with control
// characters removed.
func text(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func texts(in []string, max int) []string {
	in = capList(in)
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = text(s, max); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func labels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, min(len(keys), maxLabels))
	for _, k := range keys {
		if len(out) == maxLabels {
			break
		}
		if kk := text(k, 256); kk != "" {
			out[kk] = text(in[k], maxLabelValueLen)
		}
	}
	return out
}

func capList[T any](in []T) []T {
	if len(in) > maxListItems {
		return in[:maxListItems]
	}
	return in
}
