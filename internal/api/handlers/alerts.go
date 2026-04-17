package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/alerts"
	"github.com/dockmesh/dockmesh/internal/notify"
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
	writeJSON(w, http.StatusOK, h.Notify.Channels())
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
	writeJSON(w, http.StatusCreated, c)
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
	c, err := h.Notify.Update(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "notify.channel_update", strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, c)
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
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}
