package agents

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/i18n"
	"github.com/dockmesh/dockmesh/internal/notices"
)

// ErrUpgradeQueued is returned when an agent on a Swarm manager waits for
// another manager of the same Swarm to finish its update.
var ErrUpgradeQueued = errors.New("another manager of this Swarm is updating its agent; this one follows once it is back")

const (
	// managerHoldMax is how long the next manager waits for the previous
	// one to come back with the new version.
	managerHoldMax = 5 * time.Minute
	// While a manager holds its cluster's slot, its waits are short so a
	// slow or stalling host can't keep the other managers back for long.
	managerLockWait     = 30 * time.Second
	managerFrameTimeout = 2 * time.Minute

	managerTimeoutNoticePrefix = "agents.manager-update-timeout."
	managerDroppedNoticePrefix = "agents.manager-update-dropped."
)

// ManagerClusterFunc returns the id of the cluster agent host id is a
// linked manager of, or "".
type ManagerClusterFunc func(ctx context.Context, id string) (string, error)

// SetManagerCluster sets where the controller finds which cluster an
// agent's host manages; without it no update is held back.
func (c *UpgradeController) SetManagerCluster(f ManagerClusterFunc) { c.managerCluster = f }

// managerGate lets one agent per Swarm update at a time. An agent restart
// leaves dockerd running, so this is about Dockmesh keeping a manager to
// reach the Swarm through, not about quorum.
type managerGate struct {
	mu     sync.Mutex
	slots  map[string]chan struct{} // Swarm cluster ID → held while an update runs
	queued map[string]bool          // agent IDs waiting for their slot
}

func (g *managerGate) slot(key string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.slots == nil {
		g.slots = map[string]chan struct{}{}
	}
	s, ok := g.slots[key]
	if !ok {
		s = make(chan struct{}, 1)
		g.slots[key] = s
	}
	return s
}

func (g *managerGate) tryAcquire(key string) (release func(), ok bool) {
	s := g.slot(key)
	select {
	case s <- struct{}{}:
		return releaseOnce(s), true
	default:
		return nil, false
	}
}

func (g *managerGate) acquire(ctx context.Context, key string) (func(), error) {
	s := g.slot(key)
	select {
	case s <- struct{}{}:
		return releaseOnce(s), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func releaseOnce(s chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { <-s }) }
}

// enqueue marks id as waiting and reports false when it already was.
func (g *managerGate) enqueue(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.queued == nil {
		g.queued = map[string]bool{}
	}
	if g.queued[id] {
		return false
	}
	g.queued[id] = true
	return true
}

func (g *managerGate) dequeue(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.queued, id)
}

// managerKey returns the cluster ag's host is a linked manager of, else
// "". The link comes from the cluster's origin node list, not from what
// the agent reports, so an agent can't put itself in another Swarm's line.
// Workers and hosts outside a cluster update as before.
func (c *UpgradeController) managerKey(ctx context.Context, ag *ConnectedAgent) string {
	if c.managerCluster == nil {
		return ""
	}
	id, err := c.managerCluster(ctx, ag.ID)
	if err != nil {
		slog.Warn("agent upgrade: cluster membership unknown", "agent", ag.ID, "err", err)
		return ""
	}
	return id
}

// upgradeManager updates ag now when no other manager of its Swarm is
// updating, else queues it behind them and returns ErrUpgradeQueued.
func (c *UpgradeController) upgradeManager(ctx context.Context, ag *ConnectedAgent, req *AgentUpgradeReq, key string, manual bool) (*AgentUpgradeReq, error) {
	if release, ok := c.gate.tryAcquire(key); ok {
		return c.dispatchManager(ctx, ag, req, manual, release)
	}
	if c.gate.enqueue(ag.ID) {
		slog.Info("agent upgrade waits for another Swarm manager", "agent", ag.ID, "swarm", key)
		go c.whenSlotFree(c.baseCtx(), ag, req, key, manual) //nolint:contextcheck // the wait outlives the request
	}
	return req, ErrUpgradeQueued
}

// whenSlotFree runs on the controller's context, since the request that
// queued the update ends right away.
func (c *UpgradeController) whenSlotFree(ctx context.Context, ag *ConnectedAgent, req *AgentUpgradeReq, key string, manual bool) {
	defer c.gate.dequeue(ag.ID)
	release, err := c.gate.acquire(ctx, key)
	if err != nil {
		return
	}
	if cur := c.agents.GetConnected(ag.ID); cur != ag {
		release()
		if cur != nil && !agentIsUpToDate(cur) {
			c.droppedNotice(ctx, ag, errors.New("the agent reconnected while it waited"))
		}
		return
	}
	if _, err := c.dispatchManager(ctx, ag, req, manual, release); err != nil && !errors.Is(err, ErrAgentCurrent) {
		slog.Info("queued agent upgrade not dispatched", "agent", ag.ID, "err", err)
		c.droppedNotice(ctx, ag, err)
	}
}

func (c *UpgradeController) dispatchManager(ctx context.Context, ag *ConnectedAgent, req *AgentUpgradeReq, manual bool, release func()) (*AgentUpgradeReq, error) {
	out, err := c.send(ctx, ag, req, manual, managerLockWait, managerFrameTimeout)
	if err != nil {
		release()
		return out, err
	}
	go c.holdManager(c.baseCtx(), ag, release) //nolint:contextcheck // the hold outlives the request
	return out, nil
}

// holdManager keeps the Swarm's slot until ag is back with the server's
// build, or raises a notice for admins after managerHoldMax.
func (c *UpgradeController) holdManager(ctx context.Context, ag *ConnectedAgent, release func()) {
	defer release()
	deadline := time.NewTimer(c.managerHoldMax)
	defer deadline.Stop()
	tick := time.NewTicker(c.pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			slog.Warn("agent on a Swarm manager not back after its update; going on with the next manager", "agent", ag.ID, "wait", c.managerHoldMax)
			c.managerTimeoutNotice(ctx, ag)
			return
		case <-tick.C:
			if cur := c.agents.GetConnected(ag.ID); cur != nil && cur != ag && agentIsUpToDate(cur) {
				return
			}
		}
	}
}

func (c *UpgradeController) managerTimeoutNotice(ctx context.Context, ag *ConnectedAgent) {
	n := c.agents.notices
	if n == nil {
		return
	}
	mins := int(c.managerHoldMax / time.Minute)
	if err := n.Raise(ctx, notices.RaiseInput{
		Key:      managerTimeoutNoticePrefix + ag.ID,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.agent_manager_timeout.title", ag.Name, mins),
		Body:     i18n.Msg("notice.agent_manager_timeout.body", ag.Name, mins),
		Link:     "/hosts/" + ag.ID,
	}); err != nil {
		slog.Warn("manager update notice", "agent", ag.ID, "err", err)
	}
}

// droppedNotice tells admins that a queued upgrade of ag was not sent.
func (c *UpgradeController) droppedNotice(ctx context.Context, ag *ConnectedAgent, reason error) {
	n := c.agents.notices
	if n == nil {
		return
	}
	if err := n.Raise(ctx, notices.RaiseInput{
		Key:      managerDroppedNoticePrefix + ag.ID,
		Audience: notices.Admins(),
		Severity: notices.SevWarning,
		Title:    i18n.Msg("notice.agent_manager_dropped.title", ag.Name),
		Body:     i18n.Msg("notice.agent_manager_dropped.body", clip(reason.Error(), 300)),
		Link:     "/hosts/" + ag.ID,
	}); err != nil {
		slog.Warn("manager update notice", "agent", ag.ID, "err", err)
	}
}

// resolveManagerNotices ends the manager update notices of agent id; it
// runs when the agent connects with the server's build and when it is
// deleted.
func (s *Service) resolveManagerNotices(ctx context.Context, id string) {
	if s.notices == nil {
		return
	}
	for _, p := range []string{managerTimeoutNoticePrefix, managerDroppedNoticePrefix} {
		if err := s.notices.Resolve(ctx, p+id); err != nil {
			slog.Warn("resolve manager update notice", "agent", id, "err", err)
		}
	}
}

func (c *UpgradeController) baseCtx() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}
