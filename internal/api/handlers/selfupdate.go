package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/changelog"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/userprefs"
	"github.com/dockmesh/dockmesh/pkg/version"
)

// GetUpdateStatus returns the cached self-update check result for the
// topbar banner + Settings → System tab.
//
//	GET /api/v1/system/update-status
func (h *Handlers) GetUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if h.SelfUpdate == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":          false,
			"update_available": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, h.SelfUpdate.Status())
}

// CheckUpdateNow forces an immediate GitHub Releases lookup. Wired to
// the "Check now" button in Settings. Returns the refreshed status
// synchronously so the UI can render the new result without a second
// round-trip.
//
//	POST /api/v1/system/update-check
func (h *Handlers) CheckUpdateNow(w http.ResponseWriter, r *http.Request) {
	if h.SelfUpdate == nil {
		writeError(w, http.StatusServiceUnavailable, "update checker unavailable")
		return
	}
	if err := h.SelfUpdate.CheckNow(r.Context()); err != nil {
		// The checker records the error in its status; return 200 with
		// the body so the UI can show the message inline rather than a
		// toast from a 5xx.
		writeJSON(w, http.StatusOK, h.SelfUpdate.Status())
		return
	}
	h.audit(r, "system.update_check", "", nil)
	writeJSON(w, http.StatusOK, h.SelfUpdate.Status())
}

// ReleaseNotes is one version's entries from the changelog.
type ReleaseNotes struct {
	Version    string   `json:"version"`
	Date       string   `json:"date,omitempty"`
	Severity   string   `json:"severity,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Highlights []string `json:"highlights"`
	Added      []string `json:"added"`
	Changed    []string `json:"changed"`
	Fixed      []string `json:"fixed"`
	Security   []string `json:"security"`
}

// WhatsNew is the body of GET /system/whats-new.
type WhatsNew struct {
	Current         *ReleaseNotes `json:"current"`
	Latest          *ReleaseNotes `json:"latest"`
	LastSeenVersion string        `json:"last_seen_version"`
	Unseen          bool          `json:"unseen"`
}

// GetWhatsNew returns the release notes of the running version, taken
// from the changelog embedded at build time, and of a newer published
// release from its manifest.
//
//	GET /api/v1/system/whats-new
func (h *Handlers) GetWhatsNew(w http.ResponseWriter, r *http.Request) {
	out := WhatsNew{}
	if s := changelog.Find(h.releaseSections, version.Version); s != nil && s.Version != changelog.Unreleased {
		out.Current = notesFromSection(s)
	}
	// Whether this install lags behind a release, and which advisories it
	// misses, is shown only to callers who may see the update status.
	if h.SelfUpdate != nil && middleware.Allowed(r.Context(), rbac.PermSystemView) {
		if m := h.SelfUpdate.LatestManifest(); m != nil {
			out.Latest = notesFromManifest(m)
		}
	}
	if uid := middleware.UserID(r.Context()); uid != "" && h.UserPrefs != nil {
		seen, err := h.UserPrefs.Get(r.Context(), uid, userprefs.KeyWhatsNewSeen)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out.LastSeenVersion = seen
		out.Unseen = out.Current != nil && seen != out.Current.Version
	}
	writeJSON(w, http.StatusOK, out)
}

// MarkWhatsNewSeen records that the caller has seen the running
// version's notes.
//
//	POST /api/v1/system/whats-new/seen
func (h *Handlers) MarkWhatsNewSeen(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	if uid == "" || h.UserPrefs == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s := changelog.Find(h.releaseSections, version.Version)
	if s == nil || s.Version == changelog.Unreleased {
		writeError(w, http.StatusConflict, "the running build has no release notes")
		return
	}
	if err := h.UserPrefs.Set(r.Context(), uid, userprefs.KeyWhatsNewSeen, s.Version); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func notesFromSection(s *changelog.Section) *ReleaseNotes {
	m := changelog.BuildManifest(s)
	return notesFromManifest(&m)
}

func notesFromManifest(m *changelog.Manifest) *ReleaseNotes {
	security := append([]string{}, m.Changes.Security...)
	for _, a := range m.Advisories {
		security = append(security, a.ID+" ("+a.Severity+"): "+a.Title)
	}
	return &ReleaseNotes{
		Version:    m.Version,
		Date:       m.Date,
		Severity:   m.Severity,
		Summary:    m.Summary,
		Highlights: changelog.NonNil(m.Highlights),
		Added:      changelog.NonNil(m.Changes.Added),
		Changed:    changelog.NonNil(m.Changes.Changed),
		Fixed:      changelog.NonNil(m.Changes.Fixed),
		Security:   security,
	}
}
