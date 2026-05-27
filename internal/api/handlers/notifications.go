package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/go-chi/chi/v5"
)

// ListNotifications returns the caller's notification feed: own +
// broadcast rows, newest first. ?unread=true filters to unread only,
// ?limit=N caps the returned slice.
//
//	GET /api/v1/notifications?unread=true&limit=50
func (h *Handlers) ListNotifications(w http.ResponseWriter, r *http.Request) {
	if h.Notifications == nil {
		writeJSON(w, http.StatusOK, []notifications.Notification{})
		return
	}
	userID := middleware.UserID(r.Context())
	unread := r.URL.Query().Get("unread") == "true"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.Notifications.List(r.Context(), userID, unread, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// NotificationsUnreadCount drives the bell-icon badge. Cheap counter
// query — polled on every page load so it has to be fast.
//
//	GET /api/v1/notifications/unread-count
func (h *Handlers) NotificationsUnreadCount(w http.ResponseWriter, r *http.Request) {
	if h.Notifications == nil {
		writeJSON(w, http.StatusOK, map[string]int{"unread": 0})
		return
	}
	userID := middleware.UserID(r.Context())
	n, err := h.Notifications.UnreadCount(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread": n})
}

// MarkNotificationRead sets read_at on a single notification. The
// ownership check makes sure users can't ack each other's rows; broadcasts
// (user_id IS NULL) are markable by anyone since they're for everyone.
//
//	POST /api/v1/notifications/{id}/read
func (h *Handlers) MarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	if h.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, "notifications not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	n, err := h.Notifications.Get(r.Context(), id)
	if errors.Is(err, notifications.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Ownership: broadcast rows (UserID == "") are public; per-user
	// rows must belong to the caller.
	userID := middleware.UserID(r.Context())
	if n.UserID != "" && n.UserID != userID {
		writeError(w, http.StatusForbidden, "not your notification")
		return
	}
	if err := h.Notifications.MarkRead(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MarkAllNotificationsRead clears the unread badge for the caller —
// both per-user + broadcast rows the caller can see.
//
//	POST /api/v1/notifications/read-all
func (h *Handlers) MarkAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if h.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, "notifications not configured")
		return
	}
	if err := h.Notifications.MarkAllRead(r.Context(), middleware.UserID(r.Context())); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteNotification hard-removes a row from the user's feed. Same
// ownership rules as MarkRead: own rows + broadcasts.
//
//	DELETE /api/v1/notifications/{id}
func (h *Handlers) DeleteNotification(w http.ResponseWriter, r *http.Request) {
	if h.Notifications == nil {
		writeError(w, http.StatusServiceUnavailable, "notifications not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	n, err := h.Notifications.Get(r.Context(), id)
	if errors.Is(err, notifications.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	userID := middleware.UserID(r.Context())
	if n.UserID != "" && n.UserID != userID {
		writeError(w, http.StatusForbidden, "not your notification")
		return
	}
	if err := h.Notifications.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
