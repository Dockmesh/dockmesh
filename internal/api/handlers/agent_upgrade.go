package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/agents"
)

// GetAgentUpgradePolicy returns the current mode + fleet snapshot
// (connected / up-to-date / pending counts) so admins can gauge the
// impact of switching modes.
func (h *Handlers) GetAgentUpgradePolicy(w http.ResponseWriter, r *http.Request) {
	if h.AgentUpgrade == nil {
		writeError(w, http.StatusServiceUnavailable, "agent upgrade controller not configured")
		return
	}
	writeJSON(w, http.StatusOK, h.AgentUpgrade.Policy())
}

// UpdateAgentUpgradePolicy persists a new mode + stage params.
// Switching to `manual` clears any in-flight staged queue.
func (h *Handlers) UpdateAgentUpgradePolicy(w http.ResponseWriter, r *http.Request) {
	if h.AgentUpgrade == nil {
		writeError(w, http.StatusServiceUnavailable, "agent upgrade controller not configured")
		return
	}
	var in agents.UpgradeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	p, err := h.AgentUpgrade.SavePolicy(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "agent.upgrade_policy", in.Mode, map[string]any{
		"stage_percent": in.StagePercent,
		"stage_gap_sec": in.StageGapSec,
	})
	writeJSON(w, http.StatusOK, p)
}

// RunAgentUpgradeEvaluation kicks off a single evaluation pass of the
// configured policy against the current fleet. Useful after a server
// version bump — without waiting 60s for the next tick.
func (h *Handlers) RunAgentUpgradeEvaluation(w http.ResponseWriter, r *http.Request) {
	if h.AgentUpgrade == nil {
		writeError(w, http.StatusServiceUnavailable, "agent upgrade controller not configured")
		return
	}
	h.AgentUpgrade.Evaluate(r.Context())
	h.audit(r, "agent.upgrade_run", "", nil)
	writeJSON(w, http.StatusOK, h.AgentUpgrade.Policy())
}
