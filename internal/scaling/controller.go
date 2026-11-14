package scaling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dockmesh/dockmesh/internal/metrics"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

// TargetFunc resolves the scale target of a service of a stack.
type TargetFunc func(ctx context.Context, stack, service string) (Target, error)

// ScaleFunc scales t from the count the auto-scaler saw to a new one.
type ScaleFunc func(ctx context.Context, stack string, t Target, from, to int) error

type sampler interface {
	Query(ctx context.Context, q metrics.Query) ([]metrics.Sample, error)
}

// errCountChanged means the replica count or the deployment changed
// between evaluating a rule and taking the stack lock, e.g. by a manual
// scale.
var errCountChanged = errors.New("replica count changed")

// Controller is the auto-scaling goroutine that polls metrics every
// 30s and evaluates rules from each stack's .dockmesh.meta.json.
//
// State machine per rule:
//
//	stable → threshold_exceeded (held for duration_seconds)
//	      → scale_action → cooldown → stable
type Controller struct {
	stacks  *stacks.Manager
	metrics sampler
	targets TargetFunc
	scaleFn ScaleFunc

	pollInterval time.Duration
	mu           sync.Mutex
	states       map[string]*ruleState // key: "stack/service"
	paused       map[string]string     // stack → reason of its open paused notice
	seen         map[string]bool       // stacks checked since this process started
	stop         chan struct{}
	wg           sync.WaitGroup
}

type phase int

const (
	phaseStable   phase = iota
	phaseExceeded       // threshold breached, accumulating duration
	phaseCooldown       // just scaled, waiting for cooldown to expire
)

type ruleState struct {
	phase         phase
	direction     string // "up" or "down" while phaseExceeded
	exceededSince time.Time
	cooldownUntil time.Time
}

func NewController(sm *stacks.Manager, mc *metrics.Collector, targets TargetFunc, fn ScaleFunc) *Controller {
	c := &Controller{
		stacks:       sm,
		targets:      targets,
		scaleFn:      fn,
		pollInterval: 30 * time.Second,
		states:       make(map[string]*ruleState),
		paused:       make(map[string]string),
		seen:         make(map[string]bool),
		stop:         make(chan struct{}),
	}
	// A nil *Collector stored in the interface would not compare to nil.
	if mc != nil {
		c.metrics = mc
	}
	return c
}

func (c *Controller) Start(ctx context.Context) {
	c.wg.Add(1)
	go c.loop(ctx)
	slog.Info("scaling controller started", "interval", c.pollInterval)
}

func (c *Controller) Stop() {
	close(c.stop)
	c.wg.Wait()
}

func (c *Controller) loop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

func (c *Controller) tick(ctx context.Context) {
	for _, s := range c.stacks.List() {
		v, err := Check(c.stacks, s.Name)
		if err != nil {
			slog.Debug("scaling: check stack", "stack", s.Name, "err", err)
			continue
		}
		if v.Pause != nil {
			c.hold(ctx, s.Name, *v.Pause)
			continue
		}
		c.release(ctx, s.Name)
		if v.Rules == nil {
			continue
		}
		for _, rule := range v.Rules.Rules {
			c.evaluateRule(ctx, s.Name, rule)
		}
	}
}

// hold raises the paused notice once per stack and pause; the open
// notice is replaced when the pause changes.
func (c *Controller) hold(ctx context.Context, name string, p stacks.ScalerPause) {
	key := p.Reason + "|" + strings.Join(p.Files, ",") + "|" + p.Override + "|" + p.Error
	c.mu.Lock()
	prev, held := c.paused[name]
	c.paused[name] = key
	c.seen[name] = true
	c.mu.Unlock()
	if held && prev == key {
		return
	}
	slog.Warn("scaling: stack paused", "stack", name, "reason", p.Reason, "files", p.Files, "override", p.Override, "err", p.Error)
	if held {
		c.stacks.ReleaseScaler(ctx, name)
	}
	c.stacks.HoldScaler(ctx, name, p)
}

// release resolves the paused notice when the stack was held, or on the
// first tick after a start, when a notice may be left from before.
func (c *Controller) release(ctx context.Context, name string) {
	c.mu.Lock()
	_, held := c.paused[name]
	seen := c.seen[name]
	delete(c.paused, name)
	c.seen[name] = true
	c.mu.Unlock()
	if held || !seen {
		c.stacks.ReleaseScaler(ctx, name)
	}
}

func (c *Controller) evaluateRule(ctx context.Context, stackName string, rule Rule) {
	key := stackName + "/" + rule.Service
	c.mu.Lock()
	st, ok := c.states[key]
	if !ok {
		st = &ruleState{phase: phaseStable}
		c.states[key] = st
	}
	c.mu.Unlock()

	now := time.Now()
	if st.phase == phaseCooldown {
		if now.Before(st.cooldownUntil) {
			return
		}
		st.phase = phaseStable
	}

	t, err := c.targets(ctx, stackName, rule.Service)
	if err != nil {
		slog.Debug("scaling: resolve target", "key", key, "err", err)
		return
	}
	replicas, err := t.Replicas(ctx)
	if err != nil {
		slog.Debug("scaling: list replicas", "key", key, "err", err)
		return
	}
	current := len(replicas)
	// A stopped or never deployed service is left alone: scaling it up
	// would start a stack somebody stopped.
	if current == 0 {
		st.phase = phaseStable
		return
	}

	upMetric := rule.ScaleUp.metric("")
	up, err := c.serviceAvg(ctx, t.MetricsHost(), replicas, upMetric, rule.Unit())
	if err != nil {
		slog.Debug("scaling: metrics query", "key", key, "err", err)
		st.phase = phaseStable
		return
	}
	if up >= rule.ScaleUp.ThresholdPercent && current < rule.MaxReplicas {
		if c.held(st, "up", now, rule.ScaleUp.DurationSeconds) {
			c.doScale(ctx, stackName, rule, t, st, current, min(current+1, rule.MaxReplicas), "up", upMetric, up)
		}
		return
	}

	if rule.ScaleDown.ThresholdPercent > 0 && current > rule.MinReplicas {
		downMetric := rule.ScaleDown.metric(upMetric)
		down := up
		if downMetric != upMetric {
			down, err = c.serviceAvg(ctx, t.MetricsHost(), replicas, downMetric, rule.Unit())
			if err != nil {
				slog.Debug("scaling: metrics query", "key", key, "err", err)
				st.phase = phaseStable
				return
			}
		}
		if down <= rule.ScaleDown.ThresholdPercent {
			if c.held(st, "down", now, rule.ScaleDown.DurationSeconds) {
				c.doScale(ctx, stackName, rule, t, st, current, max(current-1, rule.MinReplicas), "down", downMetric, down)
			}
			return
		}
	}

	st.phase = phaseStable
}

// held records that a threshold in direction is crossed and reports
// whether it has been for durationSec. Crossing the other threshold
// starts the wait over.
func (c *Controller) held(st *ruleState, direction string, now time.Time, durationSec int) bool {
	if st.phase != phaseExceeded || st.direction != direction {
		st.phase = phaseExceeded
		st.direction = direction
		st.exceededSince = now
	}
	return now.Sub(st.exceededSince) >= time.Duration(durationSec)*time.Second
}

func (c *Controller) doScale(ctx context.Context, stackName string, rule Rule, t Target, st *ruleState, from, to int, direction, metric string, metricVal float64) {
	slog.Info("scaling: action",
		"stack", stackName, "service", rule.Service,
		"direction", direction, "from", from, "to", to,
		"metric", metric, "value", fmt.Sprintf("%.1f%%", metricVal))

	if err := c.scaleFn(ctx, stackName, t, from, to); err != nil {
		st.phase = phaseStable
		switch {
		case errors.Is(err, stacklock.ErrBusy), errors.Is(err, errCountChanged):
			slog.Info("scaling: retrying on the next check", "stack", stackName, "service", rule.Service, "err", err)
		default:
			slog.Warn("scaling: action failed",
				"stack", stackName, "service", rule.Service,
				"direction", direction, "err", err)
		}
		return
	}

	st.phase = phaseCooldown
	st.cooldownUntil = time.Now().Add(time.Duration(rule.CooldownSeconds) * time.Second)
}

// serviceAvg averages a metric over the samples of the last 60 seconds of
// every replica, CPU in the given unit. No samples at all is an error, so
// a service without metrics is never scaled down for looking idle.
func (c *Controller) serviceAvg(ctx context.Context, hostID string, replicas []Replica, metric, cpuUnit string) (float64, error) {
	if c.metrics == nil {
		return 0, errors.New("metrics collector unavailable")
	}
	now := time.Now()
	from := now.Add(-60 * time.Second)
	var total float64
	var count int
	for _, r := range replicas {
		if r.Name == "" {
			continue
		}
		samples, err := c.metrics.Query(ctx, metrics.Query{HostID: hostID, Name: r.Name, From: from, To: now})
		if err != nil {
			return 0, err
		}
		for _, s := range samples {
			switch metric {
			case "memory":
				if s.MemLimit > 0 {
					total += float64(s.MemUsed) / float64(s.MemLimit) * 100
				}
			default:
				total += cpuValue(s, cpuUnit)
			}
			count++
		}
	}
	if count == 0 {
		return 0, errors.New("no recent metrics for the service's replicas")
	}
	return total / float64(count), nil
}

func cpuValue(s metrics.Sample, unit string) float64 {
	if unit == CPUUnitHost {
		return s.CPUPercent
	}
	return s.CPUPercentCore
}

// CheckedScale scales under the stack lock, from the stack files as they
// are now, and only while the files allow the auto-scaler to act and the
// service still runs from replicas.
func CheckedScale(sm *stacks.Manager, locks *stacklock.Locks, deployments *stacks.DeploymentStore) ScaleFunc {
	return func(ctx context.Context, stack string, t Target, from, to int) error {
		release, err := locks.TryLock(t.LockKey(), stacklock.OpAutoScale)
		if err != nil {
			return err
		}
		defer release()
		// A migration or delete doesn't take the stack lock; its status does
		// tell, and so does a move to another host since the evaluation.
		d, err := deployments.Get(ctx, stack)
		if err != nil {
			return err
		}
		if d == nil || d.Status != "deployed" || stacklock.Host(d.HostID) != t.LockKey().Target {
			return fmt.Errorf("%w: stack %s is not deployed there", errCountChanged, stack)
		}
		v, err := Check(sm, stack)
		if err != nil {
			return err
		}
		if v.Pause != nil {
			return fmt.Errorf("auto-scaling of %s is paused: %s", stack, v.Pause.Reason)
		}
		if v.Rules == nil {
			return fmt.Errorf("stack %s has no enabled scaling rules", stack)
		}
		replicas, err := t.Replicas(ctx)
		if err != nil {
			return err
		}
		if len(replicas) != from {
			return fmt.Errorf("%w: %d running, expected %d", errCountChanged, len(replicas), from)
		}
		_, err = t.Scale(ctx, v.Files.Detail.Compose, v.Files.Detail.Env, to)
		return err
	}
}
