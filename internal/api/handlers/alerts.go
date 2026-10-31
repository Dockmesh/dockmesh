package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/notify"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// -----------------------------------------------------------------------------
// notification channels
// -----------------------------------------------------------------------------

func (h *Handlers) ListNotificationChannels(w http.ResponseWriter, r *http.Request) {
	if h.Notify == nil {
		writeJSON(w, http.StatusOK, []notify.Channel{})
		return
	}
	list := h.Notify.Channels()
	out := make([]*notify.Channel, len(list))
	for i := range list {
		out[i] = redactChannel(&list[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) CreateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.Notify == nil {
		writeError(w, http.StatusServiceUnavailable, "notify not configured")
		return
	}
	var in notify.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	c, err := h.Notify.Create(r.Context(), in)
	if errors.Is(err, notify.ErrUnknownType) {
		writeError(w, http.StatusBadRequest, "unknown channel type")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "notify.channel_create", c.Type, map[string]string{"name": c.Name})
	writeJSON(w, http.StatusCreated, redactChannel(c))
}

func (h *Handlers) UpdateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.Notify == nil {
		writeError(w, http.StatusServiceUnavailable, "notify not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in notify.ChannelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	for _, prev := range h.Notify.Channels() {
		if prev.ID == id && prev.Type == in.Type {
			cfg, err := keepConfig(in.Config, prev.Config, notifySecrets(in.Type))
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			kept, err := json.Marshal(cfg)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid config")
				return
			}
			in.Config = kept
			break
		}
	}
	c, err := h.Notify.Update(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "notify.channel_update", strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, redactChannel(c))
}

func (h *Handlers) DeleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.Notify == nil {
		writeError(w, http.StatusServiceUnavailable, "notify not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.Notify.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "notify.channel_delete", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) TestNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if h.Notify == nil {
		writeError(w, http.StatusServiceUnavailable, "notify not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	test := notify.Notification{
		Title:     "Dockmesh test notification",
		Body:      "If you see this, the channel is wired up correctly.",
		Level:     notify.LevelInfo,
		Container: "test",
		Metric:    "cpu_percent",
		Value:     42,
		Threshold: 80,
		Time:      time.Now(),
	}
	if err := h.Notify.SendTo(r.Context(), id, test); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -----------------------------------------------------------------------------
// alert rules
// -----------------------------------------------------------------------------

const errAlertScope = "out of scope: host-scoped roles may only manage rules limited to one of their hosts"

// alertRuleInScope lets a host-scoped caller manage only rules bound to a
// host they can access: an unscoped rule would deliver events from every
// host to a channel the caller picks. Metric rules watch the local host.
func (h *Handlers) alertRuleInScope(r *http.Request, kind, scopeHost string) bool {
	if !h.callerHostScoped(r) {
		return true
	}
	if kind == "" || kind == alerts.KindMetric {
		return h.hostVisible(r, "local")
	}
	if !alerts.HostBound(kind) || scopeHost == "" {
		return false
	}
	return h.hostVisible(r, scopeHost)
}

// callerHostScoped reports whether the caller's user scope or any of its
// roles restricts which hosts it may act on. A role that can't reach a
// host id that exists nowhere is scoped.
func (h *Handlers) callerHostScoped(r *http.Request) bool {
	if len(middleware.ScopeTags(r.Context())) > 0 {
		return true
	}
	return !h.checkRoleScope(r, rbac.ScopeRequest{HostID: "dockmesh-scope-probe"})
}

func (h *Handlers) hostVisible(r *http.Request, hostID string) bool {
	if !h.canAccessHost(r, hostID) {
		return false
	}
	req := rbac.ScopeRequest{HostID: hostID}
	if h.HostTags != nil {
		req.HostTags = h.HostTags.Tags(hostID)
	}
	return h.checkRoleScope(r, req)
}

func (h *Handlers) ListAlertRules(w http.ResponseWriter, r *http.Request) {
	if h.Alerts == nil {
		writeJSON(w, http.StatusOK, []alerts.Rule{})
		return
	}
	rules, err := h.Alerts.ListRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (h *Handlers) CreateAlertRule(w http.ResponseWriter, r *http.Request) {
	if h.Alerts == nil {
		writeError(w, http.StatusServiceUnavailable, "alerts not configured")
		return
	}
	var in alerts.RuleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !h.alertRuleInScope(r, in.Kind, in.ScopeHost) {
		writeError(w, http.StatusForbidden, errAlertScope)
		return
	}
	rule, err := h.Alerts.Create(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "alerts.rule_create", rule.Name, nil)
	writeJSON(w, http.StatusCreated, rule)
}

func (h *Handlers) UpdateAlertRule(w http.ResponseWriter, r *http.Request) {
	if h.Alerts == nil {
		writeError(w, http.StatusServiceUnavailable, "alerts not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in alerts.RuleInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if h.callerHostScoped(r) {
		cur, err := h.Alerts.Rule(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		if !h.alertRuleInScope(r, cur.Kind, cur.ScopeHost) || !h.alertRuleInScope(r, in.Kind, in.ScopeHost) {
			writeError(w, http.StatusForbidden, errAlertScope)
			return
		}
	}
	rule, err := h.Alerts.Update(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "alerts.rule_update", rule.Name, nil)
	writeJSON(w, http.StatusOK, rule)
}

func (h *Handlers) DeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	if h.Alerts == nil {
		writeError(w, http.StatusServiceUnavailable, "alerts not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if h.callerHostScoped(r) {
		if cur, err := h.Alerts.Rule(r.Context(), id); err == nil && !h.alertRuleInScope(r, cur.Kind, cur.ScopeHost) {
			writeError(w, http.StatusForbidden, errAlertScope)
			return
		}
	}
	if err := h.Alerts.Delete(r.Context(), id); err != nil {
		// Built-in rules are not deletable — surface 409 so the UI can
		// suggest "disable instead of delete".
		if errors.Is(err, alerts.ErrBuiltinImmutable) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "alerts.rule_delete", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// GetAlertRuleStats returns the fires-in-window summary used by the
// alerts UI to show "fired N× / 7d" counters on each rule card.
//
//	GET /api/v1/alerts/rules/{id}/stats?days=7
func (h *Handlers) GetAlertRuleStats(w http.ResponseWriter, r *http.Request) {
	if h.Alerts == nil {
		writeError(w, http.StatusServiceUnavailable, "alerts not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	stats, err := h.Alerts.RuleStatsByID(r.Context(), id, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handlers) ListAlertHistory(w http.ResponseWriter, r *http.Request) {
	if h.Alerts == nil {
		writeJSON(w, http.StatusOK, []alerts.HistoryEntry{})
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	entries, err := h.Alerts.History(r.Context(), limit)
	if err == nil && h.callerHostScoped(r) {
		visible := entries[:0]
		for _, e := range entries {
			if e.HostID != "" && h.hostVisible(r, e.HostID) {
				visible = append(visible, e)
			}
		}
		entries = visible
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}
