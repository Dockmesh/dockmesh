package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/pkg/version"
)

// Upgrade-policy setting keys (P.11.16).
const (
	UpgradeModeKey         = "agent.upgrade_mode"          // auto | manual | staged
	UpgradeStagePercentKey = "agent.upgrade_stage_percent" // 1..100
	UpgradeStageGapKey     = "agent.upgrade_stage_gap_sec" // seconds between waves

	UpgradeModeAuto   = "auto"
	UpgradeModeManual = "manual"
	UpgradeModeStaged = "staged"
)

// SettingsStore is the subset of settings.Store we need. Decoupled to
// avoid pulling the settings package into agents.
type SettingsStore interface {
	Get(key, def string) string
	Set(ctx context.Context, key, value string) error
}

// UpgradePolicy is what the handler returns — current config + fleet
// snapshot so admins can see at a glance how many hosts are on the
// server version and how many are pending.
type UpgradePolicy struct {
	Mode              string     `json:"mode"`
	StagePercent      int        `json:"stage_percent"`
	StageGapSec       int        `json:"stage_gap_sec"`
	ServerVersion     string     `json:"server_version"`
	ConnectedTotal    int        `json:"connected_total"`
	ConnectedUpToDate int        `json:"connected_up_to_date"`
	ConnectedPending  int        `json:"connected_pending"`
	LastRunAt         *time.Time `json:"last_run_at,omitempty"`
}

// UpgradeInput is the PUT body.
type UpgradeInput struct {
	Mode         string `json:"mode"`
	StagePercent int    `json:"stage_percent,omitempty"`
	StageGapSec  int    `json:"stage_gap_sec,omitempty"`
}

// UpgradeController evaluates the fleet periodically and — depending
// on the configured mode — pushes FrameReqAgentUpgrade to agents whose
// version drifts from the server's. Ships in P.11.16; before this we
// only had manual per-agent upgrades via the UI button.
type UpgradeController struct {
	agents   *Service
	settings SettingsStore

	mu         sync.Mutex
	lastRunAt  time.Time
	stageQueue []string // agent ids left to upgrade in the current staged wave
	lastWaveAt time.Time
	attempts   map[string]upgradeAttempt
	now        func() time.Time
}

// Automatic upgrades stop for an agent after maxAutoUpgradeAttempts
// dispatches of the same target binary that didn't leave it up to date,
// and are spaced at least autoUpgradeRetryGap apart. A new target binary
// resets the count; the per-host upgrade button always goes through.
const (
	maxAutoUpgradeAttempts = 3
	autoUpgradeRetryGap    = 5 * time.Minute
)

var (
	// ErrAgentNotConnected is returned when an upgrade targets an agent
	// without a live connection.
	ErrAgentNotConnected = errors.New("agent not connected")
	// ErrAgentCurrent is returned when the agent already runs the build
	// the server would send.
	ErrAgentCurrent = errors.New("the agent already runs the build this server offers")
	// ErrAgentUpgrading is returned while the agent is still replacing
	// itself after an earlier request.
	ErrAgentUpgrading = errors.New("the agent is already upgrading")
	// errUpgradeBackoff marks an automatic attempt skipped by the back-off.
	errUpgradeBackoff = errors.New("automatic upgrade backed off")
)

type upgradeAttempt struct {
	target string
	count  int
	last   time.Time
}

func NewUpgradeController(svc *Service, settings SettingsStore) *UpgradeController {
	return &UpgradeController{
		agents:   svc,
		settings: settings,
		attempts: map[string]upgradeAttempt{},
		now:      time.Now,
	}
}

// Start launches the evaluator goroutine. Evaluates every 60s.
func (c *UpgradeController) Start(ctx context.Context) {
	go c.loop(ctx)
}

func (c *UpgradeController) loop(ctx context.Context) {
	// Run once at start so a newly-upgraded server picks up `auto` mode
	// immediately without waiting for the first tick.
	c.Evaluate(ctx)
	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.Evaluate(ctx)
		}
	}
}

// Policy returns the current policy + live fleet snapshot.
func (c *UpgradeController) Policy() UpgradePolicy {
	p := UpgradePolicy{
		Mode:          c.settings.Get(UpgradeModeKey, UpgradeModeManual),
		StagePercent:  intSetting(c.settings, UpgradeStagePercentKey, 10),
		StageGapSec:   intSetting(c.settings, UpgradeStageGapKey, 300),
		ServerVersion: version.Version,
	}
	c.mu.Lock()
	if !c.lastRunAt.IsZero() {
		t := c.lastRunAt
		p.LastRunAt = &t
	}
	c.mu.Unlock()

	c.agents.mu.RLock()
	for _, ca := range c.agents.connected {
		p.ConnectedTotal++
		if agentIsUpToDate(ca) {
			p.ConnectedUpToDate++
		} else {
			p.ConnectedPending++
		}
	}
	c.agents.mu.RUnlock()
	return p
}

// SavePolicy validates + persists a new policy.
func (c *UpgradeController) SavePolicy(ctx context.Context, in UpgradeInput) (*UpgradePolicy, error) {
	switch in.Mode {
	case UpgradeModeAuto, UpgradeModeManual, UpgradeModeStaged:
	case "":
		in.Mode = UpgradeModeManual
	default:
		return nil, errors.New("invalid upgrade mode — use auto, manual, or staged")
	}
	if in.StagePercent < 0 || in.StagePercent > 100 {
		return nil, errors.New("stage_percent must be 0..100")
	}
	if in.StageGapSec < 0 {
		return nil, errors.New("stage_gap_sec must be >= 0")
	}
	if err := c.settings.Set(ctx, UpgradeModeKey, in.Mode); err != nil {
		return nil, err
	}
	if in.StagePercent > 0 {
		if err := c.settings.Set(ctx, UpgradeStagePercentKey, strconv.Itoa(in.StagePercent)); err != nil {
			return nil, err
		}
	}
	if in.StageGapSec > 0 {
		if err := c.settings.Set(ctx, UpgradeStageGapKey, strconv.Itoa(in.StageGapSec)); err != nil {
			return nil, err
		}
	}
	// Clear any in-flight stage queue when the mode changes — we
	// don't want a lingering queue to surprise-upgrade nodes after
	// the admin switched to `manual`.
	c.mu.Lock()
	c.stageQueue = nil
	c.mu.Unlock()
	p := c.Policy()
	return &p, nil
}

// Evaluate runs one pass of the policy. Callers: the 60s loop and the
// manual `/agents/upgrade-policy/run` endpoint.
func (c *UpgradeController) Evaluate(ctx context.Context) {
	c.mu.Lock()
	c.lastRunAt = time.Now()
	mode := c.settings.Get(UpgradeModeKey, UpgradeModeManual)
	c.mu.Unlock()

	pending := c.pendingAgents()
	if len(pending) == 0 {
		return
	}

	switch mode {
	case UpgradeModeAuto:
		// Push upgrade frames to all pending agents in parallel.
		for _, a := range pending {
			go func() { _, _ = c.upgradeOne(ctx, a, false) }()
		}
	case UpgradeModeStaged:
		c.stagedWave(ctx, pending)
	case UpgradeModeManual:
		// No-op; UI buttons drive upgrades individually.
	}
}

// stagedWave fires the upgrade frames of the next wave.
func (c *UpgradeController) stagedWave(ctx context.Context, pending []*ConnectedAgent) {
	wave := c.nextWave(pendingIDs(pending))
	for _, id := range wave {
		ag := c.agents.GetConnected(id)
		if ag == nil {
			continue
		}
		go func() { _, _ = c.upgradeOne(ctx, ag, false) }()
	}
}

// nextWave picks the next stage-percent of the pending agents, or none
// while the stage gap since the previous wave has not passed. The queue
// persists across calls so every pending agent is reached over several
// ticks instead of the same sample being picked each time. Waves still
// start on the 60 s evaluator tick, so a gap below that acts as 60 s.
func (c *UpgradeController) nextWave(pending []string) []string {
	percent := intSetting(c.settings, UpgradeStagePercentKey, 10)
	gap := time.Duration(intSetting(c.settings, UpgradeStageGapKey, 300)) * time.Second

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.lastWaveAt.IsZero() && now.Sub(c.lastWaveAt) < gap {
		return nil
	}
	if len(c.stageQueue) == 0 {
		c.stageQueue = pending
	}
	// The wave size is a share of all pending agents, not of the ones
	// left, so the waves stay the same size; at least one agent.
	waveSize := max(len(pending)*percent/100, 1)
	waveSize = min(waveSize, len(c.stageQueue))
	wave := c.stageQueue[:waveSize]
	c.stageQueue = c.stageQueue[waveSize:]
	if len(wave) > 0 {
		c.lastWaveAt = now
		slog.Info("staged upgrade wave", "size", len(wave), "remaining", len(c.stageQueue), "gap_sec", int(gap.Seconds()))
	}
	return wave
}

// UpgradeOne is the handler-facing wrapper — sends the upgrade frame
// to a single agent by id, bypassing the automatic back-off.
func (c *UpgradeController) UpgradeOne(ctx context.Context, id string) (*AgentUpgradeReq, error) {
	ag := c.agents.GetConnected(id)
	if ag == nil {
		return nil, ErrAgentNotConnected
	}
	return c.upgradeOne(ctx, ag, true)
}

// buildUpgradeReq picks the bundled binary for the agent's architecture
// and attaches its SHA-256, which the agent verifies before replacing
// itself.
func (c *UpgradeController) buildUpgradeReq(ag *ConnectedAgent) (*AgentUpgradeReq, error) {
	arch := ag.Hello.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	name, err := AgentBinaryName(arch)
	if err != nil {
		return nil, err
	}
	sum, err := AgentBinarySHA256(arch)
	if err != nil {
		return nil, err
	}
	return &AgentUpgradeReq{
		BinaryURL: c.agents.PublicURL() + "/install/" + name,
		Version:   version.Version,
		SHA256:    sum,
	}, nil
}

// allowAttempt records an upgrade dispatch for the agent and reports
// whether it may proceed. Manual attempts are always allowed but still
// counted, so the automatic loop doesn't immediately retry behind them.
func (c *UpgradeController) allowAttempt(agentID, target string, manual bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	a := c.attempts[agentID]
	if a.target != target {
		a = upgradeAttempt{target: target}
	}
	if !manual {
		if a.count >= maxAutoUpgradeAttempts {
			return false
		}
		if !a.last.IsZero() && now.Sub(a.last) < autoUpgradeRetryGap {
			return false
		}
	}
	a.count++
	a.last = now
	c.attempts[agentID] = a
	return true
}

func (c *UpgradeController) upgradeOne(ctx context.Context, ag *ConnectedAgent, manual bool) (*AgentUpgradeReq, error) {
	req, err := c.buildUpgradeReq(ag)
	if err != nil {
		slog.Warn("agent upgrade: no binary to offer", "agent", ag.ID, "arch", ag.Hello.Arch, "err", err)
		return nil, err
	}
	if !c.allowAttempt(ag.ID, req.SHA256, manual) {
		return nil, errUpgradeBackoff
	}
	payload, _ := json.Marshal(req)
	resp, err := ag.Request(ctx, Frame{Type: FrameReqAgentUpgrade, Payload: payload})
	if err != nil {
		// The agent answers refusals with a request error.
		if r := upgradeRefusal(err.Error()); errors.Is(r, ErrAgentCurrent) || errors.Is(r, ErrAgentUpgrading) {
			return req, r
		}
		slog.Warn("agent upgrade push failed", "agent", ag.ID, "err", err)
		return req, err
	}
	if !resp.OK {
		err := upgradeRefusal(resp.Error)
		if !errors.Is(err, ErrAgentCurrent) && !errors.Is(err, ErrAgentUpgrading) {
			slog.Warn("agent upgrade rejected", "agent", ag.ID, "err", resp.Error)
		}
		return req, err
	}
	slog.Info("agent upgrade dispatched", "agent", ag.ID, "version", version.Version)
	return req, nil
}

// upgradeRefusal maps an agent's refusal to an error, by the texts of
// errSameBinary and errUpgradeBusy in cmd/dockmesh-agent.
func upgradeRefusal(msg string) error {
	switch {
	case strings.Contains(msg, "already running this agent build"):
		return ErrAgentCurrent
	case strings.Contains(msg, "an upgrade is already in progress"):
		return ErrAgentUpgrading
	}
	return errors.New(msg)
}

func (c *UpgradeController) pendingAgents() []*ConnectedAgent {
	var out []*ConnectedAgent
	c.agents.mu.RLock()
	defer c.agents.mu.RUnlock()
	for _, ca := range c.agents.connected {
		if !agentIsUpToDate(ca) {
			out = append(out, ca)
		}
	}
	return out
}

// agentIsUpToDate is the single source of truth for "does this agent
// match the server build". Commit hash is the canonical identity —
// Version is a human label that can (and did) include a +<hash>
// suffix after the ldflags rework, which broke the naive string
// compare the controller used to do. Older agents that predate the
// Commit field fall back to the Version compare so they still upgrade
// during the rollout transition.
func agentIsUpToDate(ca *ConnectedAgent) bool {
	if ca.Hello.Commit != "" && version.Commit != "" && version.Commit != "none" {
		return ca.Hello.Commit == version.Commit
	}
	return ca.Hello.Version == version.Version
}

// Outdated returns the connected agents whose build differs from the
// server's, keyed by agent id, with the version each agent reports.
func (s *Service) Outdated() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]string{}
	for id, ca := range s.connected {
		if !agentIsUpToDate(ca) {
			out[id] = ca.Hello.Version
		}
	}
	return out
}

func pendingIDs(list []*ConnectedAgent) []string {
	out := make([]string, len(list))
	for i, ca := range list {
		out[i] = ca.ID
	}
	return out
}

func intSetting(s SettingsStore, key string, def int) int {
	v := s.Get(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
