package handlers

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// ScanImage runs the scanner against the image and stores the result.
func (h *Handlers) ScanImage(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	if h.Scanner == nil || h.ScanStore == nil {
		writeError(w, http.StatusServiceUnavailable, "scanner not configured")
		return
	}
	if err := h.Scanner.Ready(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	scopeReq := h.hostScopeReq(r.Context(), "local")
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermImagesScan, scopeReq, "host local")
		return
	}

	id, _ := url.PathUnescape(chi.URLParam(r, "id"))
	// Resolve image id → repo:tag the scanner understands. Fall back to id.
	ref := id
	info, err := h.Docker.InspectImage(r.Context(), id)
	if err == nil && len(info.RepoTags) > 0 {
		ref = info.RepoTags[0]
	}

	rep, err := h.Scanner.Scan(r.Context(), ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.ScanStore.Save(r.Context(), rep); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionImageScan, ref, map[string]int{
		"critical": rep.Summary.Critical,
		"high":     rep.Summary.High,
		"total":    rep.Summary.Total(),
	})

	// Bell-icon notification — only for findings that actually warrant
	// attention (HIGH or CRITICAL). Lower-severity vulns flow through
	// the cached report; the bell stays useful instead of crying wolf.
	if h.Notifications != nil && (rep.Summary.Critical > 0 || rep.Summary.High > 0) {
		sev := notifications.SevWarning
		if rep.Summary.Critical > 0 {
			sev = notifications.SevError
		}
		_, _ = h.Notifications.Emit(r.Context(), notifications.EmitInput{
			Kind:     notifications.KindCVEFound,
			Severity: sev,
			Title:    fmt.Sprintf("CVE scan: %s", ref),
			Body:     fmt.Sprintf("%d critical, %d high", rep.Summary.Critical, rep.Summary.High),
			Link:     "/resources?tab=images",
		})
	}
	writeJSON(w, http.StatusOK, rep)
}

// GetScan returns the cached scan result for an image, or 404.
func (h *Handlers) GetScan(w http.ResponseWriter, r *http.Request) {
	if h.ScanStore == nil {
		writeError(w, http.StatusServiceUnavailable, "scanner not configured")
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	id, _ := url.PathUnescape(chi.URLParam(r, "id"))
	ref := id
	info, err := h.Docker.InspectImage(r.Context(), id)
	if err == nil && len(info.RepoTags) > 0 {
		ref = info.RepoTags[0]
	}
	rep, err := h.ScanStore.Get(r.Context(), ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rep == nil {
		writeError(w, http.StatusNotFound, "no scan cached")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
