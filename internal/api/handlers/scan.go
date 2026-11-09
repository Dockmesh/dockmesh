package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/notifications"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/scanner"
	"github.com/dockmesh/dockmesh/internal/settings"
	"github.com/go-chi/chi/v5"
)

// scanTransferTimeout bounds receiving an image from an agent, on top of
// the scanner's own timeout.
const scanTransferTimeout = 30 * time.Minute

// scanRef resolves the image on target to the reference its report is
// filed under: the first tag, or the image id for an untagged image.
func scanRef(ctx context.Context, target host.Host, id string) (string, error) {
	info, err := target.InspectImage(ctx, id)
	if err != nil {
		return "", err
	}
	if len(info.RepoTags) > 0 && info.RepoTags[0] != "<none>:<none>" {
		return info.RepoTags[0], nil
	}
	return info.ID, nil
}

// scanOn scans an image of target. The scanner reads the server's own
// daemon directly; from any other host the image is fetched as an archive,
// so agents need neither grype nor its vulnerability database.
func scanOn(ctx context.Context, s scanner.Scanner, target host.Host, ref string) (*scanner.Report, error) {
	exporter, remote := target.(host.ImageExporter)
	if !remote {
		return s.Scan(ctx, ref)
	}
	ctx, cancel := context.WithTimeout(ctx, scanTransferTimeout)
	defer cancel()
	archive, err := exporter.ExportImage(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer archive.Close()
	return s.ScanArchive(ctx, archive, ref)
}

func (h *Handlers) scannerEnabled() bool {
	return h.Settings == nil || h.Settings.GetBool(settings.KeyScannerEnabled, true)
}

// scannerHealth reports what a scan would do right now. A missing binary
// is "off", not a warning: installs made with DOCKMESH_NO_SCANNER=1 have
// none on purpose.
func (h *Handlers) scannerHealth() HealthCheck {
	c := HealthCheck{Name: "scanner", Label: "CVE scanner", LinkTo: "/images", Status: "off"}
	switch {
	case !h.scannerEnabled():
		c.Detail = "disabled"
	case h.Scanner == nil:
		c.Detail = "not configured"
	case h.Scanner.Ready() != nil:
		c.Detail = "enabled, but the Grype binary was not found"
	default:
		c.Status = "ok"
		c.Detail = "Grype ready"
	}
	return c
}

// ScanImage runs the scanner against an image on the host named by ?host=
// and stores the result.
func (h *Handlers) ScanImage(w http.ResponseWriter, r *http.Request) {
	if h.Scanner == nil || h.ScanStore == nil {
		writeError(w, http.StatusServiceUnavailable, "scanner not configured")
		return
	}
	if !h.scannerEnabled() {
		writeError(w, http.StatusServiceUnavailable, "image scanning is turned off under Settings → General")
		return
	}
	if err := h.Scanner.Ready(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	scopeReq := h.hostScopeReq(target.ID())
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermImagesScan, scopeReq, "host "+target.ID())
		return
	}

	id, _ := url.PathUnescape(chi.URLParam(r, "id"))
	ref, err := scanRef(r.Context(), target, id)
	if err != nil {
		writeError(w, imageErrorStatus(err), err.Error())
		return
	}

	rep, err := scanOn(r.Context(), h.Scanner, target, ref)
	if err != nil {
		writeError(w, imageErrorStatus(err), err.Error())
		return
	}
	if err := h.ScanStore.Save(r.Context(), target.ID(), rep); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionImageScan, ref, map[string]any{
		"critical": rep.Summary.Critical,
		"high":     rep.Summary.High,
		"total":    rep.Summary.Total(),
		"host":     target.ID(),
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
			Perm:     rbac.PermImagesView,
			HostID:   target.ID(),
		})
	}
	writeJSON(w, http.StatusOK, rep)
}

// GetScan returns the cached scan result for an image on the host named by
// ?host=, or {"scanned":false} when it has never been scanned there.
func (h *Handlers) GetScan(w http.ResponseWriter, r *http.Request) {
	if h.ScanStore == nil {
		writeError(w, http.StatusServiceUnavailable, "scanner not configured")
		return
	}
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	scopeReq := h.hostScopeReq(target.ID())
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermImagesScan, scopeReq, "host "+target.ID())
		return
	}
	id, _ := url.PathUnescape(chi.URLParam(r, "id"))
	ref, err := scanRef(r.Context(), target, id)
	if err != nil {
		writeError(w, imageErrorStatus(err), err.Error())
		return
	}
	rep, err := h.ScanStore.Get(r.Context(), target.ID(), ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rep == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"scanned": false})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
