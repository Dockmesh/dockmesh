package handlers

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/settings"
)

// ListSettings returns all DB-backed settings for the System tab.
//
//	GET /api/v1/settings
func (h *Handlers) ListSettings(w http.ResponseWriter, r *http.Request) {
	if h.Settings == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, publicSettings(h.Settings.All()))
}

// The audit webhook keys are managed through /audit/webhook, which is gated
// by audit.write; the HMAC secret must never be readable and the URL can
// carry a receiver token.
var auditWebhookSettingKeys = map[string]bool{
	audit.WebhookURLKey:    true,
	audit.WebhookSecretKey: true,
	audit.WebhookFilterKey: true,
}

func publicSettings(all []settings.Entry) []settings.Entry {
	out := make([]settings.Entry, 0, len(all))
	for _, e := range all {
		if !auditWebhookSettingKeys[e.Key] {
			out = append(out, e)
		}
	}
	return out
}

// UpdateSettings writes one or more settings and, for toggle keys like
// `proxy_enabled`, fires the lifecycle action (container up / down) so
// the DB flag and the actual runtime state stay in sync.
//
// Before FINDING-53 this handler was DB-only, which turned the Settings
// → System "Reverse Proxy" toggle into a dead-end: DB said "enabled"
// but the Caddy container never started, and the /proxy page kept
// telling users to "enable it in Settings" — a loop with no way out.
//
//	PUT /api/v1/settings
//	Body: [{"key":"proxy_enabled","value":"true"}, ...]
func (h *Handlers) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}
	var entries []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &entries); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	for _, e := range entries {
		if auditWebhookSettingKeys[e.Key] {
			writeError(w, http.StatusBadRequest, e.Key+" is managed via /audit/webhook")
			return
		}
		if e.Key == settings.KeyTrustedProxies {
			if _, err := clientip.ParseList(e.Value); err != nil {
				writeError(w, http.StatusBadRequest, "trusted proxies: "+err.Error())
				return
			}
		}
	}
	for _, e := range entries {
		// Where local backups may be written is as powerful as replacing the
		// server binary, so it needs that permission rather than settings
		// access.
		if (e.Key == targets.LocalRootsSetting || e.Key == targets.LocalRootsSeededSetting) && !roleAllows(r, rbac.PermSystemUpgrade) {
			writeError(w, http.StatusForbidden, "changing "+e.Key+" needs the system.upgrade permission")
			return
		}
		if e.Key == targets.LocalRootsSetting {
			if err := h.Settings.Set(r.Context(), targets.LocalRootsSeededSetting, "reviewed"); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		prev := h.Settings.Get(e.Key, "")
		if err := h.Settings.Set(r.Context(), e.Key, e.Value); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if prev == e.Value {
			continue
		}
		h.applySettingSideEffect(r, e.Key, e.Value)
	}
	writeJSON(w, http.StatusOK, publicSettings(h.Settings.All()))
}

// applySettingSideEffect runs the lifecycle hooks tied to a setting
// change. Failures are logged, not surfaced — the DB write already
// succeeded and the user can retry via the dedicated endpoint (e.g.
// POST /proxy/enable) if the side effect fails for, say, a missing
// Docker daemon.
func (h *Handlers) applySettingSideEffect(r *http.Request, key, value string) {
	switch key {
	case settings.KeyTrustedProxies:
		h.proxyNotice.reset(r.Context())
	case "proxy_enabled":
		if h.Proxy == nil {
			return
		}
		truthy := strings.EqualFold(value, "true")
		if truthy {
			if err := h.Proxy.EnableProxy(r.Context()); err != nil {
				slog.Error("settings: proxy enable failed", "err", err)
				return
			}
			h.audit(r, "proxy.enable", "", nil)
		} else {
			if err := h.Proxy.DisableProxy(r.Context()); err != nil {
				slog.Error("settings: proxy disable failed", "err", err)
				return
			}
			h.audit(r, "proxy.disable", "", nil)
		}
	}
}
