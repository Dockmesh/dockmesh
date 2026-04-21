package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/host"
)

// ContainerSummary is the aggregated container snapshot the dashboard
// needs — counts by state + per-stack rollup — without the ~15 KB of
// full container objects that /containers returns.
//
// Before this endpoint the dashboard called /containers?all=true every
// 10 s to compute four counters. At ~15 KB and 26 ms of server work
// per call, that's 90 KB/min of payload just to render four numbers.
// Summary does the aggregation server-side, returns ~1 KB, and shaves
// the `/containers` hot path off the dashboard's auto-refresh.
type ContainerSummary struct {
	Total      int                     `json:"total"`
	Running    int                     `json:"running"`
	Stopped    int                     `json:"stopped"`
	Unhealthy  int                     `json:"unhealthy"`
	ByStack    map[string]StackRollup  `json:"by_stack"`
}

// StackRollup is the per-stack aggregation consumed by the dashboard's
// stack grid. Tracks counts + the service names so the card can render
// "3 services" without needing the full container list.
type StackRollup struct {
	Total     int      `json:"total"`
	Running   int      `json:"running"`
	Unhealthy int      `json:"unhealthy"`
	Hosts     []string `json:"hosts"`    // distinct host_ids this stack runs on
	Services  []string `json:"services"` // compose service names
}

// ContainerSummaryEndpoint returns the aggregated view. Query params:
//   ?host=local   default — local docker only
//   ?host=<id>    specific agent
//   ?host=all     fan out across every online host, merge rollups
//
//	GET /api/v1/containers/summary
func (h *Handlers) ContainerSummaryEndpoint(w http.ResponseWriter, r *http.Request) {
	hostID := r.URL.Query().Get("host")

	// All-mode: fan out. Each host returns a ContainerSummary; we merge
	// them into a single summary so the dashboard doesn't have to.
	if host.IsAll(hostID) && h.Hosts != nil {
		targets := h.Hosts.PickAll(r.Context())
		targets = h.filterHostsByScope(r, targets)
		merged := ContainerSummary{ByStack: map[string]StackRollup{}}
		for _, t := range targets {
			s, err := h.summaryForHost(r.Context(), t)
			if err != nil {
				continue
			}
			merged.Total += s.Total
			merged.Running += s.Running
			merged.Stopped += s.Stopped
			merged.Unhealthy += s.Unhealthy
			for name, r := range s.ByStack {
				cur := merged.ByStack[name]
				cur.Total += r.Total
				cur.Running += r.Running
				cur.Unhealthy += r.Unhealthy
				cur.Hosts = mergeDistinct(cur.Hosts, r.Hosts)
				cur.Services = mergeDistinct(cur.Services, r.Services)
				merged.ByStack[name] = cur
			}
		}
		writeJSON(w, http.StatusOK, merged)
		return
	}

	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	s, err := h.summaryForHost(r.Context(), target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// summaryForHost lists containers for one host and computes the summary.
// Docker's List call still has to return every container (that's how
// Docker's API works), but we aggregate server-side and emit a tiny
// response instead of shipping the raw list over the wire.
func (h *Handlers) summaryForHost(ctx context.Context, target host.Host) (ContainerSummary, error) {
	list, err := target.ListContainers(ctx, true)
	if err != nil {
		return ContainerSummary{}, err
	}
	out := ContainerSummary{ByStack: map[string]StackRollup{}}
	hostID := target.ID()
	for _, c := range list {
		out.Total++
		switch c.State {
		case "running":
			out.Running++
		case "exited", "dead", "created":
			out.Stopped++
		}
		if strings.Contains(strings.ToLower(c.Status), "unhealthy") {
			out.Unhealthy++
		}
		project := c.Labels["com.docker.compose.project"]
		service := c.Labels["com.docker.compose.service"]
		if project == "" {
			continue
		}
		r := out.ByStack[project]
		r.Total++
		if c.State == "running" {
			r.Running++
		}
		if strings.Contains(strings.ToLower(c.Status), "unhealthy") {
			r.Unhealthy++
		}
		r.Hosts = addDistinct(r.Hosts, hostID)
		if service != "" {
			r.Services = addDistinct(r.Services, service)
		}
		out.ByStack[project] = r
	}
	return out, nil
}

func addDistinct(slice []string, val string) []string {
	for _, s := range slice {
		if s == val {
			return slice
		}
	}
	return append(slice, val)
}

func mergeDistinct(a, b []string) []string {
	for _, v := range b {
		a = addDistinct(a, v)
	}
	return a
}
