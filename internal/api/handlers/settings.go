package handlers

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/backup"
	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/clientip"
	"github.com/dockmesh/dockmesh/internal/proxy"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/selfupdate"
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
		// The settings page sends every key back; only a change is refused.
		if slices.Contains(proxy.ManagedSettingKeys, e.Key) && h.Settings.Get(e.Key, "") != e.Value {
			writeError(w, http.StatusBadRequest, e.Key+" is managed via /proxy/settings and /proxy/version")
			return
		}
		// Invite links and sign-in callbacks are built from the base URL, so
		// pointing it elsewhere is as powerful as handing out invites.
		if e.Key == settings.KeyBaseURL && h.Settings.Stored(e.Key) != e.Value {
			if !middleware.AccessOf(r.Context()).AllowsGlobally(rbac.PermUsersCreate) {
				writeError(w, http.StatusForbidden, "changing base_url needs the users.create permission as well, because invite links and sign-in callbacks use it")
				return
			}
			if e.Value != "" {
				if err := agents.ValidateBaseURL(e.Value); err != nil {
					writeError(w, http.StatusBadRequest, "base URL "+err.Error())
					return
				}
			}
		}
		// Every install command is built from the agent URL, so pointing
		// it elsewhere is as powerful as enrolling hosts.
		if e.Key == settings.KeyAgentPublicURL && h.Settings.Stored(e.Key) != e.Value {
			if !middleware.AccessOf(r.Context()).AllowsGlobally(rbac.PermHostsCreate) {
				writeError(w, http.StatusForbidden, "changing agent_public_url needs the hosts.create permission as well, because install commands use it")
				return
			}
			if e.Value != "" {
				if err := agents.ValidateAgentURL(e.Value); err != nil {
					writeError(w, http.StatusBadRequest, "agent URL "+err.Error())
					return
				}
			}
		}
		// The channel decides which builds admins are told to install.
		if e.Key == selfupdate.ChannelKey {
			if !selfupdate.ValidChannel(e.Value) {
				writeError(w, http.StatusBadRequest, "update_channel must be stable or prerelease")
				return
			}
			if h.Settings.Stored(e.Key) != e.Value && !middleware.AccessOf(r.Context()).AllowsGlobally(rbac.PermSystemUpgrade) {
				writeError(w, http.StatusForbidden, "changing update_channel needs the system.upgrade permission")
				return
			}
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
		if (e.Key == targets.LocalRootsSetting || e.Key == targets.LocalRootsSeededSetting || e.Key == targets.EmptyPathSetting) && !middleware.AccessOf(r.Context()).AllowsGlobally(rbac.PermSystemUpgrade) {
			writeError(w, http.StatusForbidden, "changing "+e.Key+" needs the system.upgrade permission")
			return
		}
		if e.Key == backup.TimezoneSetting {
			if err := backup.CheckTimezone(e.Value); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if e.Key == targets.LocalRootsSetting {
			if err := h.Settings.Set(r.Context(), targets.LocalRootsSeededSetting, "reviewed"); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		prev := h.Settings.Get(e.Key, "")
		if e.Key == settings.KeyBaseURL || e.Key == settings.KeyAgentPublicURL || e.Key == selfupdate.ChannelKey {
			prev = h.Settings.Stored(e.Key)
		}
		if err := h.Settings.Set(r.Context(), e.Key, e.Value); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if prev == e.Value {
			continue
		}
		switch e.Key {
		case settings.KeyBaseURL:
			h.audit(r, "settings.base_url_update", "", map[string]any{"before": prev, "after": e.Value})
		case settings.KeyAgentPublicURL:
			h.audit(r, "settings.agent_public_url_update", "", map[string]any{"before": prev, "after": e.Value})
		case selfupdate.ChannelKey:
			h.audit(r, "settings.update_channel_update", "", map[string]any{"before": prev, "after": e.Value})
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
	case settings.KeyBaseURL, settings.KeyAgentPublicURL:
		if h.Agents != nil {
			if _, err := h.Agents.ReloadEndpoints(); err != nil {
				slog.Error("settings: agent URL", "err", err)
			}
		}
	case settings.KeyTrustedProxies:
		h.proxyNotice.reset(r.Context())
	case backup.TimezoneSetting:
		if h.Backups != nil {
			if err := h.Backups.SetTimezone(r.Context(), value); err != nil {
				slog.Error("settings: backup time zone", "err", err)
			}
		}
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
