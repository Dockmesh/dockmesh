package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strconv"
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
	Mode             string `json:"mode"`
	StagePercent     int    `json:"stage_percent"`
	StageGapSec      int    `json:"stage_gap_sec"`
	ServerVersion    string `json:"server_version"`
	ConnectedTotal   int    `json:"connected_total"`
	ConnectedUpToDate int   `json:"connected_up_to_date"`
	ConnectedPending int    `json:"connected_pending"`
	LastRunAt        *time.Time `json:"last_run_at,omitempty"`
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
	stageQueue []string  // agent ids left to upgrade in the current staged wave
}

func NewUpgradeController(svc *Service, settings SettingsStore) *UpgradeController {
	return &UpgradeController{agents: svc, settings: settings}
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
		if ca.Hello.Version == version.Version {
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
			go c.upgradeOne(ctx, a)
		}
	case UpgradeModeStaged:
		c.stagedWave(ctx, pending)
	case UpgradeModeManual:
		// No-op; UI buttons drive upgrades individually.
	}
}

// stagedWave picks the next N% of agents to upgrade and fires their
// frames. The queue persists across calls so we walk through every
// pending agent over multiple ticks rather than re-picking the same
// sample each time.
func (c *UpgradeController) stagedWave(ctx context.Context, pending []*ConnectedAgent) {
	percent := intSetting(c.settings, UpgradeStagePercentKey, 10)
	gap := intSetting(c.settings, UpgradeStageGapKey, 300)

	c.mu.Lock()
	if len(c.stageQueue) == 0 {
		// Rebuild the queue when empty or after mode switched.
		c.stageQueue = pendingIDs(pending)
	}
	// Wave size: at least 1, at most percent% of the ORIGINAL pending
	// set (not the current remaining). Rough — but staged rollout
	// precision isn't a customer feature, it's a blast-radius limit.
	waveSize := len(pending) * percent / 100
	if waveSize < 1 {
		waveSize = 1
	}
	if waveSize > len(c.stageQueue) {
		waveSize = len(c.stageQueue)
	}
	wave := c.stageQueue[:waveSize]
	c.stageQueue = c.stageQueue[waveSize:]
	c.mu.Unlock()

	slog.Info("staged upgrade wave",
		"size", len(wave), "remaining", len(c.stageQueue), "gap_sec", gap)

	for _, id := range wave {
		ag := c.agents.GetConnected(id)
		if ag == nil {
			continue
		}
		go c.upgradeOne(ctx, ag)
	}
	// Note: we don't sleep here for `gap` seconds — the outer 60s
	// tick already provides a natural gap. Operators tuning for
	// aggressive rollouts can lower settings; for slower rollouts
	// they raise gap (which won't take effect until the 60s loop
	// ticks anyway, so the perceived minimum interval is 60s). Good
	// enough for P.11.16; a proper scheduler can come later.
	_ = gap
}

// UpgradeOne is the handler-facing wrapper — sends the upgrade frame
// to a single agent by id.
func (c *UpgradeController) UpgradeOne(ctx context.Context, id string) error {
	ag := c.agents.GetConnected(id)
	if ag == nil {
		return errors.New("agent not connected")
	}
	return c.upgradeOne(ctx, ag)
}

func (c *UpgradeController) upgradeOne(ctx context.Context, ag *ConnectedAgent) error {
	arch := ag.Hello.Arch
	if arch == "" {
		arch = runtime.GOARCH
	}
	binaryName := "dockmesh-agent-linux-" + arch
	binaryURL := c.agents.publicURL + "/install/" + binaryName
	req := AgentUpgradeReq{BinaryURL: binaryURL, Version: version.Version}
	payload, _ := json.Marshal(req)
	resp, err := ag.Request(ctx, Frame{Type: FrameReqAgentUpgrade, Payload: payload})
	if err != nil {
		slog.Warn("agent upgrade push failed", "agent", ag.ID, "err", err)
		return err
	}
	if !resp.OK {
		slog.Warn("agent upgrade rejected", "agent", ag.ID, "err", resp.Error)
		return errors.New(resp.Error)
	}
	slog.Info("agent upgrade dispatched", "agent", ag.ID, "version", version.Version)
	return nil
}

func (c *UpgradeController) pendingAgents() []*ConnectedAgent {
	var out []*ConnectedAgent
	c.agents.mu.RLock()
	defer c.agents.mu.RUnlock()
	for _, ca := range c.agents.connected {
		if ca.Hello.Version != version.Version {
			out = append(out, ca)
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
