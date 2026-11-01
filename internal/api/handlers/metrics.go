package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/docker"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// Empty-state reasons of a host without stored history.
const (
	historyReasonOutdated = "agent_outdated" // its agent predates history
	historyReasonOffline  = "agent_offline"  // offline, nothing stored yet
)

// containerMetricsResponse is the stored history of one container.
type containerMetricsResponse struct {
	HostID       string           `json:"host_id"`
	History      bool             `json:"history"`
	Reason       string           `json:"reason,omitempty"`
	MemUnlimited bool             `json:"mem_unlimited"`
	Samples      []metrics.Sample `json:"samples"`
}

// hostMetricsResponse is the stored history of one host.
type hostMetricsResponse struct {
	HostID  string               `json:"host_id"`
	History bool                 `json:"history"`
	Reason  string               `json:"reason,omitempty"`
	Samples []metrics.HostSample `json:"samples"`
}

type containerLatest struct {
	metrics.Latest
	ID           string `json:"id"`
	Stack        string `json:"stack,omitempty"`
	MemUnlimited bool   `json:"mem_unlimited"`
}

// batchStatsResponse is the newest sample of every running container of
// one host.
type batchStatsResponse struct {
	HostID     string            `json:"host_id"`
	History    bool              `json:"history"`
	Reason     string            `json:"reason,omitempty"`
	Containers []containerLatest `json:"containers"`
}

// hostHistory reports whether hostID records or has stored history, and
// the empty-state reason when not. The server host always records.
func (h *Handlers) hostHistory(ctx context.Context, hostID string) (bool, string, error) {
	if hostID == metrics.LocalHostID {
		return true, "", nil
	}
	var live *agents.ConnectedAgent
	if h.Agents != nil {
		live = h.Agents.GetConnected(hostID)
	}
	if live != nil && live.RecordsHistory() {
		return true, "", nil
	}
	ok, err := h.Metrics.HasHistory(ctx, hostID)
	if err != nil || ok {
		return ok, "", err
	}
	if live != nil {
		return false, historyReasonOutdated, nil
	}
	return false, historyReasonOffline, nil
}

// hostCapacity returns the core count and RAM of a host, zeros when it
// does not answer in time.
func hostCapacity(ctx context.Context, target host.Host) (int, uint64) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	m, err := target.SystemMetrics(ctx)
	if err != nil {
		return 0, 0
	}
	return m.CPUCores, m.MemTotal
}

// knownHost reports whether hostID is the server or an enrolled agent.
func (h *Handlers) knownHost(ctx context.Context, hostID string) bool {
	if hostID == metrics.LocalHostID {
		return true
	}
	if h.Agents == nil {
		return false
	}
	a, err := h.Agents.Get(ctx, hostID)
	return err == nil && a != nil
}

// Newest-sample windows of the batch stats: the server samples every
// 30 s; an agent's samples arrive up to 90 s later, with its minute
// batch.
const (
	latestLocal = 60
	latestAgent = 180
)

// BatchContainerStats returns the newest CPU + memory sample of every
// running container on the host.
//
//	GET /api/v1/hosts/{id}/stats/containers
func (h *Handlers) BatchContainerStats(w http.ResponseWriter, r *http.Request) {
	if h.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics not configured")
		return
	}
	hostID := chi.URLParam(r, "id")
	if !h.requireHostAccess(w, r, hostID) {
		return
	}
	if !h.knownHost(r.Context(), hostID) {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	hist, reason, err := h.hostHistory(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := batchStatsResponse{HostID: hostID, History: hist, Reason: reason, Containers: []containerLatest{}}
	if !hist {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	target, err := h.hostByID(hostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	list, err := target.ListContainers(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	list = h.filterContainersByRoleScope(r, hostID, list)
	byName := make(map[string]containerLatest, len(list))
	for _, c := range list {
		if len(c.Names) == 0 {
			continue
		}
		byName[strings.TrimPrefix(c.Names[0], "/")] = containerLatest{
			ID: c.ID, Stack: c.Labels["com.docker.compose.project"],
		}
	}
	cores, memTotal := hostCapacity(r.Context(), target)
	maxAge := int64(latestAgent)
	if hostID == metrics.LocalHostID {
		maxAge = latestLocal
	}
	latest, err := h.Metrics.LatestPerContainer(r.Context(), hostID, maxAge, cores)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, l := range latest {
		row, ok := byName[l.Name]
		if !ok {
			continue
		}
		row.Latest = l
		row.MemUnlimited = docker.NoMemLimit(l.MemLimit, memTotal)
		resp.Containers = append(resp.Containers, row)
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseHistoryQuery reads from / to (unix seconds) and resolution
// (raw | 1m | 1h); defaults are the last hour at raw resolution.
func parseHistoryQuery(r *http.Request) metrics.Query {
	q := metrics.Query{Resolution: r.URL.Query().Get("resolution")}
	if v := r.URL.Query().Get("from"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			q.From = time.Unix(n, 0)
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			q.To = time.Unix(n, 0)
		}
	}
	return q
}

// GetMetrics returns the stored samples of a container on ?host=
// (default the server). History is keyed by container name, so it
// survives the recreate of an update.
func (h *Handlers) GetMetrics(w http.ResponseWriter, r *http.Request) {
	if h.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics not configured")
		return
	}
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if !h.requireContainerScope(w, r, rbac.PermContainersView, target.ID(), id) {
		return
	}
	info, err := target.InspectContainer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	hist, reason, err := h.hostHistory(r.Context(), target.ID())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := containerMetricsResponse{HostID: target.ID(), History: hist, Reason: reason, Samples: []metrics.Sample{}}
	if !hist {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	q := parseHistoryQuery(r)
	q.HostID = target.ID()
	q.Name = strings.TrimPrefix(info.Name, "/")
	cores, memTotal := hostCapacity(r.Context(), target)
	q.Cores = cores
	samples, err := h.Metrics.Query(r.Context(), q)
	if err != nil {
		writeHistoryError(w, err)
		return
	}
	resp.Samples = samples
	if info.HostConfig != nil && info.HostConfig.Memory >= 0 {
		resp.MemUnlimited = docker.NoMemLimit(uint64(info.HostConfig.Memory), memTotal) //nolint:gosec // checked non-negative
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetHostMetrics returns the stored CPU, memory and disk samples of a
// host.
//
//	GET /api/v1/hosts/{id}/metrics
func (h *Handlers) GetHostMetrics(w http.ResponseWriter, r *http.Request) {
	if h.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics not configured")
		return
	}
	hostID := chi.URLParam(r, "id")
	if !h.inReadScope(r, hostID, "") {
		writeError(w, http.StatusForbidden, "out of scope: your role does not include this host")
		return
	}
	if !h.knownHost(r.Context(), hostID) {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	hist, reason, err := h.hostHistory(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := hostMetricsResponse{HostID: hostID, History: hist, Reason: reason, Samples: []metrics.HostSample{}}
	if !hist {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	q := parseHistoryQuery(r)
	q.HostID = hostID
	samples, err := h.Metrics.QueryHost(r.Context(), q)
	if err != nil {
		writeHistoryError(w, err)
		return
	}
	resp.Samples = samples
	writeJSON(w, http.StatusOK, resp)
}

func writeHistoryError(w http.ResponseWriter, err error) {
	if errors.Is(err, metrics.ErrResolution) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
