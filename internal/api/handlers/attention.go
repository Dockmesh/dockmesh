package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// AttentionItem is one entry of GET /attention.
type AttentionItem struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Severity string          `json:"severity"`
	Title    string          `json:"title"`
	Detail   string          `json:"detail,omitempty"`
	Link     string          `json:"link"`
	Object   AttentionObject `json:"object"`
	Since    *time.Time      `json:"since,omitempty"`
}

// AttentionObject names the resource an item is about.
type AttentionObject struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	HostID string `json:"host_id,omitempty"`
}

const (
	attentionCritical = "critical"
	attentionWarning  = "warning"
	attentionInfo     = "info"
)

// Certificates closer to expiry than these get a warning or critical item.
const (
	certWarnDays     = 30
	certCriticalDays = 7
)

var exitCodeRe = regexp.MustCompile(`^Exited \((-?\d+)\)`)

// Attention aggregates what needs someone to act across the caller's
// scope. Each source is only consulted when the caller holds its view
// permission, and every item is filtered by host and role scope, so the
// route itself needs no permission beyond authentication.
//
//	GET /api/v1/attention
func (h *Handlers) Attention(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	allowed := func(perm rbac.Perm) bool { return middleware.Allowed(ctx, perm) }
	var rows []containerRow
	if allowed(rbac.PermContainersView) || allowed(rbac.PermAlertsView) || allowed(rbac.PermImagesView) {
		rows = h.attentionContainers(r)
	}
	items := []AttentionItem{}
	if allowed(rbac.PermHostsView) {
		items = append(items, h.attentionHosts(r)...)
	}
	if allowed(rbac.PermContainersView) {
		items = append(items, attentionContainerItems(rows)...)
	}
	if allowed(rbac.PermBackupsView) {
		items = append(items, h.attentionBackups(r)...)
	}
	if allowed(rbac.PermAlertsView) {
		items = append(items, h.attentionAlerts(r, rows)...)
	}
	if allowed(rbac.PermImagesView) {
		items = append(items, h.attentionImageUpdates(rows, allowed(rbac.PermContainersView))...)
	}
	if allowed(rbac.PermProxyView) && h.callerUnscoped(r) {
		items = append(items, h.attentionCerts(r)...)
	}
	items = append(items, h.attentionNotices(r)...)
	sortAttention(items)
	writeJSON(w, http.StatusOK, items)
}

// attentionContainers lists every container on the reachable hosts the
// caller may see. The rows also decide which alerts and image updates
// are in scope.
func (h *Handlers) attentionContainers(r *http.Request) []containerRow {
	var all []containerRow
	if h.attentionRows != nil {
		all = h.attentionRows(r.Context())
	} else if h.Hosts != nil {
		all = listContainerRows(r.Context(), h.filterHostsByScope(r, h.Hosts.PickAll(r.Context())))
	}
	var rows []containerRow
	for _, c := range all {
		if h.canAccessHost(r, c.HostID) {
			rows = append(rows, c)
		}
	}
	return h.filterContainerRowsByRoleScope(r, rows)
}

func listContainerRows(ctx context.Context, targets []host.Host) []containerRow {
	res := host.FanOut(ctx, targets, func(ctx context.Context, hh host.Host) ([]containerRow, error) {
		list, err := hh.ListContainers(ctx, true)
		if err != nil {
			return nil, err
		}
		rows := make([]containerRow, len(list))
		for i, c := range list {
			rows[i] = containerRow{
				Container:    c,
				HostID:       hh.ID(),
				HostName:     hh.Name(),
				HealthStatus: parseHealthFromStatus(c.Status),
			}
		}
		return rows, nil
	})
	return res.Items
}

func (h *Handlers) attentionHosts(r *http.Request) []AttentionItem {
	if h.Agents == nil {
		return nil
	}
	list, err := h.Agents.List(r.Context())
	if err != nil {
		slog.Warn("attention: list agents", "err", err)
		return nil
	}
	outdated := h.Agents.Outdated()
	var out []AttentionItem
	for _, a := range list {
		if !h.inReadScope(r, a.ID, "") {
			continue
		}
		obj := AttentionObject{Type: "host", ID: a.ID, Name: a.Name, HostID: a.ID}
		link := "/hosts/" + url.PathEscape(a.ID)
		switch {
		case a.Status == "offline":
			out = append(out, AttentionItem{
				ID:       "host_offline:" + a.ID,
				Kind:     "host_offline",
				Severity: attentionCritical,
				Title:    a.Name + " is offline",
				Detail:   "The agent is not connected.",
				Link:     link,
				Object:   obj,
				Since:    a.LastSeenAt,
			})
		case a.Status == "online":
			v, ok := outdated[a.ID]
			if !ok {
				continue
			}
			out = append(out, AttentionItem{
				ID:       "agent_outdated:" + a.ID,
				Kind:     "agent_outdated",
				Severity: attentionWarning,
				Title:    a.Name + " runs a different agent version",
				Detail:   fmt.Sprintf("Agent %s, server %s.", orUnknown(v), version.Version),
				Link:     link,
				Object:   obj,
			})
		}
	}
	return out
}

func attentionContainerItems(rows []containerRow) []AttentionItem {
	var out []AttentionItem
	for _, c := range rows {
		name := containerName(c.Names)
		kind, severity, title := "", "", ""
		switch {
		case c.State == "restarting":
			kind, severity, title = "container_restarting", attentionCritical, name+" keeps restarting"
		case c.HealthStatus == "unhealthy":
			kind, severity, title = "container_unhealthy", attentionWarning, name+" is unhealthy"
		case c.State == "exited" && exitCode(c.Status) != 0:
			kind, severity, title = "container_exited", attentionWarning, name+" exited with an error"
		default:
			continue
		}
		link := "/containers/" + url.PathEscape(c.ID)
		if c.HostID != "" && c.HostID != "local" {
			link += "?host=" + url.QueryEscape(c.HostID)
		}
		out = append(out, AttentionItem{
			ID:       kind + ":" + c.HostID + ":" + c.ID,
			Kind:     kind,
			Severity: severity,
			Title:    title,
			Detail:   c.Status + " on " + c.HostName + ".",
			Link:     link,
			Object:   AttentionObject{Type: "container", ID: c.ID, Name: name, HostID: c.HostID},
		})
	}
	return out
}

// exitCode reads the code from Docker's "Exited (N) 3 hours ago"; 0 when
// the status has none.
func exitCode(status string) int {
	m := exitCodeRe.FindStringSubmatch(status)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func (h *Handlers) attentionBackups(r *http.Request) []AttentionItem {
	if h.Backups == nil {
		return nil
	}
	jobs, err := h.Backups.ListJobs(r.Context())
	if err != nil {
		slog.Warn("attention: list backup jobs", "err", err)
		return nil
	}
	runs, err := h.Backups.LatestRuns(r.Context())
	if err != nil {
		slog.Warn("attention: latest backup runs", "err", err)
		return nil
	}
	last := make(map[int64]backup.Run, len(runs))
	for _, run := range runs {
		last[run.JobID] = run
	}
	var out []AttentionItem
	for _, j := range jobs {
		run, ok := last[j.ID]
		if !j.Enabled || !ok || run.Status != "failed" || !h.backupJobVisible(r, j) {
			continue
		}
		since := run.StartedAt
		if run.FinishedAt != nil {
			since = *run.FinishedAt
		}
		detail := firstLine(run.Error)
		if detail == "" {
			detail = "The last run failed."
		}
		jobID := strconv.FormatInt(j.ID, 10)
		out = append(out, AttentionItem{
			ID:       "backup_failed:" + jobID,
			Kind:     "backup_failed",
			Severity: attentionCritical,
			Title:    "Backup " + j.Name + " failed",
			Detail:   detail,
			Link:     "/backups/runs/" + strconv.FormatInt(run.ID, 10),
			Object:   AttentionObject{Type: "backup_job", ID: jobID, Name: j.Name, HostID: j.HostID},
			Since:    &since,
		})
	}
	return out
}

// backupJobVisible applies the job's host and stack sources to the
// caller's scope; system jobs are admin-only like their runs.
func (h *Handlers) backupJobVisible(r *http.Request, j backup.Job) bool {
	if backup.HasSystemSource(j.Sources) {
		return h.callerIsAdmin(r)
	}
	hostID := j.HostID
	if hostID == "" {
		hostID = "local"
	}
	if len(j.Sources) == 0 {
		return h.inReadScope(r, hostID, "")
	}
	for _, src := range j.Sources {
		stack := ""
		if src.Type == "stack" {
			stack = src.Name
		}
		if !h.inReadScope(r, hostID, stack) {
			return false
		}
	}
	return true
}

// attentionAlerts lists firing alerts. Alert state is keyed by container
// name only, so scoped callers see an alert only when a container of
// that name is in their scope.
func (h *Handlers) attentionAlerts(r *http.Request, rows []containerRow) []AttentionItem {
	if h.Alerts == nil {
		return nil
	}
	firing, err := h.Alerts.Firing(r.Context())
	if err != nil {
		slog.Warn("attention: firing alerts", "err", err)
		return nil
	}
	unscoped := h.callerUnscoped(r)
	visible := map[string]bool{}
	for _, c := range rows {
		visible[containerName(c.Names)] = true
	}
	var out []AttentionItem
	for _, f := range firing {
		if !unscoped && !visible[f.ContainerName] {
			continue
		}
		severity := f.Severity
		if severity != attentionCritical && severity != attentionInfo {
			severity = attentionWarning
		}
		since := f.Since
		out = append(out, AttentionItem{
			ID:       "alert_firing:" + strconv.FormatInt(f.RuleID, 10) + ":" + f.ContainerName,
			Kind:     "alert_firing",
			Severity: severity,
			Title:    f.RuleName + " is firing",
			Detail:   f.Message,
			Link:     "/alerts",
			Object:   AttentionObject{Type: "container", ID: f.ContainerName, Name: f.ContainerName},
			Since:    &since,
		})
	}
	return out
}

// attentionImageUpdates groups the visible containers by image and lists
// the images with a newer upstream manifest.
func (h *Handlers) attentionImageUpdates(rows []containerRow, showNames bool) []AttentionItem {
	if h.UpdateWatcher == nil {
		return nil
	}
	users := map[string][]string{}
	var images []string
	for _, c := range rows {
		info, ok := h.UpdateWatcher.Get(c.Image)
		if !ok || !info.UpdateAvailable {
			continue
		}
		if _, seen := users[info.Image]; !seen {
			images = append(images, info.Image)
		}
		users[info.Image] = append(users[info.Image], containerName(c.Names))
	}
	out := make([]AttentionItem, 0, len(images))
	for _, img := range images {
		names := users[img]
		var detail string
		switch {
		case !showNames:
			detail = fmt.Sprintf("Used by %d container%s.", len(names), plural(len(names)))
		case len(names) > 1:
			detail = fmt.Sprintf("Used by %s and %d more.", names[0], len(names)-1)
		default:
			detail = "Used by " + names[0] + "."
		}
		out = append(out, AttentionItem{
			ID:       "image_update:" + img,
			Kind:     "image_update",
			Severity: attentionInfo,
			Title:    "Update available for " + img,
			Detail:   detail,
			Link:     "/resources?tab=images",
			Object:   AttentionObject{Type: "image", ID: img, Name: img},
		})
	}
	return out
}

func (h *Handlers) attentionCerts(r *http.Request) []AttentionItem {
	if h.Proxy == nil || !h.Proxy.Enabled() {
		return nil
	}
	routes, err := h.Proxy.RouteCerts(r.Context())
	if err != nil {
		slog.Warn("attention: proxy routes", "err", err)
		return nil
	}
	var out []AttentionItem
	for _, rt := range routes {
		if !rt.Enabled || rt.CertDaysLeft == nil || rt.CertValidTo == nil || *rt.CertDaysLeft > certWarnDays {
			continue
		}
		days := *rt.CertDaysLeft
		severity := attentionWarning
		if days <= certCriticalDays {
			severity = attentionCritical
		}
		title := fmt.Sprintf("Certificate for %s expires in %d days", rt.Host, days)
		switch {
		case !rt.CertValidTo.After(time.Now()):
			title = "Certificate for " + rt.Host + " has expired"
		case days == 1:
			title = "Certificate for " + rt.Host + " expires in 1 day"
		case days == 0:
			title = "Certificate for " + rt.Host + " expires today"
		}
		routeID := strconv.FormatInt(rt.ID, 10)
		out = append(out, AttentionItem{
			ID:       "cert_expiring:" + routeID,
			Kind:     "cert_expiring",
			Severity: severity,
			Title:    title,
			Detail:   "Valid until " + rt.CertValidTo.UTC().Format("2006-01-02") + ", issued by " + orUnknown(rt.CertIssuer) + ".",
			Link:     "/proxy",
			Object:   AttentionObject{Type: "proxy_route", ID: routeID, Name: rt.Host},
		})
	}
	return out
}

func (h *Handlers) attentionNotices(r *http.Request) []AttentionItem {
	if h.Notices == nil {
		return nil
	}
	list, err := h.Notices.ForViewer(r.Context(), h.noticeViewer(r))
	if err != nil {
		slog.Warn("attention: notices", "err", err)
		return nil
	}
	out := make([]AttentionItem, 0, len(list))
	for _, n := range list {
		id := strconv.FormatInt(n.ID, 10)
		since := n.CreatedAt
		out = append(out, AttentionItem{
			ID:       "notice:" + id,
			Kind:     "notice",
			Severity: string(n.Severity),
			Title:    n.Title,
			Detail:   firstLine(n.Body),
			Link:     n.Link,
			Object:   AttentionObject{Type: "notice", ID: id, Name: n.Key},
			Since:    &since,
		})
	}
	return out
}

func severityRank(s string) int {
	switch s {
	case attentionCritical:
		return 0
	case attentionWarning:
		return 1
	default:
		return 2
	}
}

// sortAttention orders by severity, then newest first; items without a
// time go after those with one.
func sortAttention(items []AttentionItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		switch {
		case a.Since != nil && b.Since != nil && !a.Since.Equal(*b.Since):
			return a.Since.After(*b.Since)
		case (a.Since == nil) != (b.Since == nil):
			return a.Since != nil
		}
		return a.ID < b.ID
	})
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 200
	if rs := []rune(s); len(rs) > max {
		s = string(rs[:max]) + "…"
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
