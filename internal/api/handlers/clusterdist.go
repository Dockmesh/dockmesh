package handlers

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// Why a node shows no CPU or memory.
const (
	noMetricsNoAgent      = "no_agent"
	noMetricsUnreachable  = "unreachable"
	noMetricsAgentOffline = "agent_offline"
	noMetricsNoAccess     = "no_access"
)

// Heavy-node rule: a node that counts runs at least heavyFactor times and
// at least heavyMargin tasks more than the fair share of the nodes that
// count (ready and active).
const (
	heavyFactor = 1.5
	heavyMargin = 3
)

const (
	labelSwarmServiceID = "com.docker.swarm.service.id"
	labelSwarmTaskID    = "com.docker.swarm.task.id"
)

type distPinned struct {
	Service string `json:"service"`
	Tasks   int    `json:"tasks"`
}

type distNode struct {
	agents.SwarmNode
	HostID     string   `json:"host_id,omitempty"`
	Tasks      int      `json:"tasks"`
	CPUPercent *float64 `json:"cpu_percent,omitempty"`
	MemPercent *float64 `json:"mem_percent,omitempty"`
	NoMetrics  string   `json:"no_metrics,omitempty"`
	// Counted nodes (ready and active) make up the fair share.
	Counted bool         `json:"counted"`
	Heavy   bool         `json:"heavy,omitempty"`
	Pinned  []distPinned `json:"pinned,omitempty"`
}

type distService struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Stack    string         `json:"stack,omitempty"`
	PerNode  map[string]int `json:"per_node"`
	Running  int            `json:"running"`
	CPUCores *float64       `json:"cpu_cores,omitempty"`
	MemBytes *uint64        `json:"mem_bytes,omitempty"`
	// Partial is set when running tasks sit on nodes whose containers
	// were not measured (no agent, agent offline, no metrics).
	Partial bool `json:"partial,omitempty"`
}

type distribution struct {
	ReadHost     string        `json:"read_host,omitempty"`
	Nodes        []distNode    `json:"nodes"`
	Tasks        int           `json:"tasks"`
	CountedNodes int           `json:"counted_nodes"`
	FairShare    float64       `json:"fair_share"`
	Services     []distService `json:"services,omitempty"`
	Truncated    bool          `json:"truncated,omitempty"`
	// Stale answers carry the node list of the last successful check and
	// no tasks: no manager could be read now.
	Stale   bool       `json:"stale,omitempty"`
	KnownAt *time.Time `json:"known_at,omitempty"`
}

// nodeUsage is a node host's CPU and memory, when its agent answered.
type nodeUsage struct {
	cpu, mem float64
}

// svcUsage is what a service's task containers on measured nodes use.
type svcUsage struct {
	cores float64
	mem   uint64
}

// distInput is everything distribute works from. services is nil when the
// caller may not see services; usage and measured are nil without usage.
type distInput struct {
	nodes    []agents.SwarmNode
	tasks    []agents.SwarmTask
	services []agents.SwarmService
	hostOf   map[string]string // node id → linked host id
	usage    map[string]nodeUsage
	offline  map[string]bool // node id → its agent did not answer
	// denied are the nodes whose host the caller holds no hosts.view on;
	// a cluster role covers the Swarm, not the hosts that are its nodes.
	denied   map[string]bool
	measured map[string]map[string]svcUsage
}

var pinRe = regexp.MustCompile(`^\s*node\.(hostname|id)\s*==\s*(\S+)\s*$`)

// pinnedTo reports whether a constraint ties a service's tasks to node n.
func pinnedTo(constraints []string, n agents.SwarmNode) bool {
	for _, c := range constraints {
		m := pinRe.FindStringSubmatch(c)
		if m == nil {
			continue
		}
		if (m[1] == "hostname" && n.Hostname != "" && m[2] == n.Hostname) || (m[1] == "id" && m[2] == n.ID) {
			return true
		}
	}
	return false
}

func distribute(in distInput) distribution {
	d := distribution{Nodes: make([]distNode, 0, len(in.nodes))}
	running := map[string]map[string]int{} // node → service → tasks
	for _, t := range in.tasks {
		if t.State != "running" || t.NodeID == "" {
			continue
		}
		if running[t.NodeID] == nil {
			running[t.NodeID] = map[string]int{}
		}
		running[t.NodeID][t.ServiceID]++
	}
	svcByID := make(map[string]agents.SwarmService, len(in.services))
	for _, s := range in.services {
		svcByID[s.ID] = s
	}
	sum := 0
	for _, n := range in.nodes {
		dn := distNode{SwarmNode: n, HostID: in.hostOf[n.ID]}
		for _, c := range running[n.ID] {
			dn.Tasks += c
		}
		dn.Counted = n.State == "ready" && n.Availability == "active"
		if n.State == "ready" {
			d.Tasks += dn.Tasks
		}
		if dn.Counted {
			d.CountedNodes++
			sum += dn.Tasks
		}
		switch u, ok := in.usage[n.ID]; {
		case dn.HostID == "":
			dn.NoMetrics = noMetricsNoAgent
		case n.State != "ready":
			dn.NoMetrics = noMetricsUnreachable
		case in.denied[n.ID]:
			dn.NoMetrics = noMetricsNoAccess
		case ok:
			cpu, mem := u.cpu, u.mem
			dn.CPUPercent, dn.MemPercent = &cpu, &mem
		case in.offline[n.ID]:
			dn.NoMetrics = noMetricsAgentOffline
		}
		d.Nodes = append(d.Nodes, dn)
	}
	if d.CountedNodes > 0 {
		d.FairShare = float64(sum) / float64(d.CountedNodes)
	}
	for i := range d.Nodes {
		dn := &d.Nodes[i]
		t := float64(dn.Tasks)
		dn.Heavy = dn.Counted && d.FairShare > 0 && t >= heavyFactor*d.FairShare && t >= d.FairShare+heavyMargin
		if !dn.Heavy || in.services == nil {
			continue
		}
		for id, c := range running[dn.ID] {
			if s, ok := svcByID[id]; ok && pinnedTo(s.Constraints, dn.SwarmNode) {
				dn.Pinned = append(dn.Pinned, distPinned{Service: s.Name, Tasks: c})
			}
		}
		sort.Slice(dn.Pinned, func(a, b int) bool { return dn.Pinned[a].Service < dn.Pinned[b].Service })
	}
	if in.services != nil {
		d.Services = distServices(in, running)
	}
	return d
}

func distServices(in distInput, running map[string]map[string]int) []distService {
	out := make([]distService, 0, len(in.services))
	for _, s := range in.services {
		ds := distService{ID: s.ID, Name: s.Name, Stack: s.Stack, PerNode: map[string]int{}}
		var cores float64
		var mem uint64
		seen := false
		for _, n := range in.nodes {
			c := running[n.ID][s.ID]
			if c == 0 {
				continue
			}
			ds.PerNode[n.ID] = c
			ds.Running += c
			if u, ok := in.measured[n.ID]; ok {
				cores += u[s.ID].cores
				mem += u[s.ID].mem
				seen = true
			} else if in.measured != nil {
				ds.Partial = true
			}
		}
		if seen {
			ds.CPUCores, ds.MemBytes = &cores, &mem
		}
		out = append(out, ds)
	}
	return out
}

// ClusterDistribution shows how the cluster's running tasks spread over
// its nodes, with each node host's CPU and memory and, for callers with
// stacks.view on the cluster, the tasks and usage of every service.
// usage=false leaves out the metrics, for the host list.
//
//	GET /api/v1/clusters/{id}/distribution
func (h *Handlers) ClusterDistribution(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if h.Clusters == nil || h.Hosts == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	c, err := h.Clusters.Get(r.Context(), id)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	in := distInput{hostOf: map[string]string{}}
	for _, m := range c.Members {
		in.hostOf[m.NodeID] = m.HostID
	}
	readHost, err := h.Clusters.ReadHost(r.Context(), id)
	if err == nil {
		in.nodes, err = h.Hosts.SwarmNodes(r.Context(), readHost)
	}
	if err != nil {
		nodes, known, ok := h.Clusters.LastNodes(id)
		if !ok {
			writeClusterReadError(w, err)
			return
		}
		in.nodes = nodes
		d := distribute(in)
		d.Stale, d.KnownAt = true, &known
		writeJSON(w, http.StatusOK, d)
		return
	}
	tasks, err := h.Hosts.SwarmTasks(r.Context(), readHost, "")
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	in.tasks = tasks.Tasks
	withUsage := r.URL.Query().Get("usage") != "false"
	withServices := withUsage && middleware.AllowedOn(r.Context(), rbac.PermStacksView, rbac.Resource{ClusterID: id})
	if withServices {
		svcs, err := h.Hosts.SwarmServices(r.Context(), readHost)
		if err != nil {
			writeClusterReadError(w, err)
			return
		}
		in.services = svcs.Services
		if in.services == nil {
			in.services = []agents.SwarmService{}
		}
	}
	if withUsage {
		h.measureNodes(r.Context(), &in, withServices, func(ctx context.Context, hostID string) bool {
			return middleware.AllowedOn(ctx, rbac.PermHostsView, h.hostScopeReq(hostID))
		})
	}
	d := distribute(in)
	d.ReadHost = readHost
	d.Truncated = tasks.Truncated
	writeJSON(w, http.StatusOK, d)
}

// measureNodes asks every ready node host the caller may view for its CPU
// and memory and, with services, sums the newest samples of its task
// containers per service.
func (h *Handlers) measureNodes(ctx context.Context, in *distInput, withServices bool, canView func(ctx context.Context, hostID string) bool) {
	in.usage, in.offline, in.denied = map[string]nodeUsage{}, map[string]bool{}, map[string]bool{}
	if withServices && h.Metrics != nil {
		in.measured = map[string]map[string]svcUsage{}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range in.nodes {
		hostID := in.hostOf[n.ID]
		if hostID == "" || n.State != "ready" {
			continue
		}
		if !canView(ctx, hostID) {
			in.denied[n.ID] = true
			continue
		}
		wg.Add(1)
		go func(nodeID, hostID string) {
			defer wg.Done()
			u, svc, ok := h.measureHost(ctx, hostID, in.measured != nil)
			mu.Lock()
			defer mu.Unlock()
			if !ok {
				in.offline[nodeID] = true
				return
			}
			in.usage[nodeID] = u
			if svc != nil {
				in.measured[nodeID] = svc
			}
		}(n.ID, hostID)
	}
	wg.Wait()
}

func (h *Handlers) measureHost(ctx context.Context, hostID string, withServices bool) (nodeUsage, map[string]svcUsage, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	target, err := h.hostByID(hostID)
	if err != nil {
		return nodeUsage{}, nil, false
	}
	sm, err := target.SystemMetrics(ctx)
	if err != nil {
		return nodeUsage{}, nil, false
	}
	u := nodeUsage{cpu: sm.CPUPercent, mem: sm.MemPercent}
	if !withServices {
		return u, nil, true
	}
	if hist, _, err := h.hostHistory(ctx, hostID); err != nil || !hist {
		return u, nil, true
	}
	list, err := target.ListContainers(ctx, false)
	if err != nil {
		return u, nil, true
	}
	svcOf := map[string]string{}
	for _, c := range list {
		if sid := c.Labels[labelSwarmServiceID]; sid != "" && c.Labels[labelSwarmTaskID] != "" && len(c.Names) > 0 {
			svcOf[strings.TrimPrefix(c.Names[0], "/")] = sid
		}
	}
	maxAge := int64(latestAgent)
	if hostID == metrics.LocalHostID {
		maxAge = latestLocal
	}
	latest, err := h.Metrics.LatestPerContainer(ctx, hostID, maxAge, sm.CPUCores)
	if err != nil {
		return u, nil, true
	}
	out := map[string]svcUsage{}
	for _, l := range latest {
		sid, ok := svcOf[l.Name]
		if !ok {
			continue
		}
		v := out[sid]
		v.cores += l.CPUPercentCore / 100
		v.mem += l.MemUsed
		out[sid] = v
	}
	return u, out, true
}
