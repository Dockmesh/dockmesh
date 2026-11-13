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

// ScaleFunc is the callback the controller calls to execute a scale
// action. Decoupled so the controller doesn't depend on the host
// package. Injected from cmd/dockmesh/main.go.
type ScaleFunc func(ctx context.Context, stackName, service string, replicas int) error

// Controller is the auto-scaling goroutine that polls metrics every
// 30s and evaluates rules from each stack's .dockmesh.meta.json.
//
// State machine per rule:
//
//	stable → threshold_exceeded (held for duration_seconds)
//	      → scale_action → cooldown → stable
type Controller struct {
	stacks  *stacks.Manager
	metrics *metrics.Collector
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
	exceededSince time.Time
	cooldownUntil time.Time
	lastReplicas  int
}

func NewController(sm *stacks.Manager, mc *metrics.Collector, fn ScaleFunc) *Controller {
	return &Controller{
		stacks:       sm,
		metrics:      mc,
		scaleFn:      fn,
		pollInterval: 30 * time.Second,
		states:       make(map[string]*ruleState),
		paused:       make(map[string]string),
		seen:         make(map[string]bool),
		stop:         make(chan struct{}),
	}
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

	// Cooldown phase — skip evaluation.
	if st.phase == phaseCooldown {
		if now.Before(st.cooldownUntil) {
			return
		}
		st.phase = phaseStable
	}

	// Detect stateful services — refuse to auto-scale.
	// (CheckScale is too expensive here, so just skip known DB images
	// by checking the rule; the handler validates at config time.)

	// Query recent metrics for this service's containers.
	avg, err := c.serviceAvg(ctx, stackName, rule.Service, rule.ScaleUp.Metric, rule.Unit())
	if err != nil {
		slog.Debug("scaling: metrics query", "key", key, "err", err)
		return
	}

	// Current replica count (from metrics sample count or StackStatus).
	currentReplicas := st.lastReplicas
	if currentReplicas <= 0 {
		currentReplicas = 1
	}

	// Evaluate scale-up threshold.
	if avg >= rule.ScaleUp.ThresholdPercent && currentReplicas < rule.MaxReplicas {
		switch st.phase {
		case phaseStable:
			st.phase = phaseExceeded
			st.exceededSince = now
		case phaseExceeded:
			if now.Sub(st.exceededSince) >= time.Duration(rule.ScaleUp.DurationSeconds)*time.Second {
				newReplicas := currentReplicas + 1
				if newReplicas > rule.MaxReplicas {
					newReplicas = rule.MaxReplicas
				}
				c.doScale(ctx, stackName, rule, key, st, currentReplicas, newReplicas, "up", avg)
			}
		}
		return
	}

	// Evaluate scale-down threshold.
	if rule.ScaleDown.ThresholdPercent > 0 && avg <= rule.ScaleDown.ThresholdPercent && currentReplicas > rule.MinReplicas {
		switch st.phase {
		case phaseStable:
			st.phase = phaseExceeded
			st.exceededSince = now
		case phaseExceeded:
			if now.Sub(st.exceededSince) >= time.Duration(rule.ScaleDown.DurationSeconds)*time.Second {
				newReplicas := currentReplicas - 1
				if newReplicas < rule.MinReplicas {
					newReplicas = rule.MinReplicas
				}
				c.doScale(ctx, stackName, rule, key, st, currentReplicas, newReplicas, "down", avg)
			}
		}
		return
	}

	// Neither threshold hit — reset to stable.
	if st.phase == phaseExceeded {
		st.phase = phaseStable
	}
}

func (c *Controller) doScale(ctx context.Context, stackName string, rule Rule, key string, st *ruleState, from, to int, direction string, metricVal float64) {
	slog.Info("scaling: action",
		"stack", stackName, "service", rule.Service,
		"direction", direction, "from", from, "to", to,
		"metric", rule.ScaleUp.Metric, "value", fmt.Sprintf("%.1f%%", metricVal))

	if err := c.scaleFn(ctx, stackName, rule.Service, to); err != nil {
		if errors.Is(err, stacklock.ErrBusy) {
			slog.Info("scaling: stack busy, retrying on the next check", "stack", stackName, "service", rule.Service, "err", err)
			st.phase = phaseStable
			return
		}
		slog.Warn("scaling: action failed",
			"stack", stackName, "service", rule.Service,
			"direction", direction, "err", err)
		st.phase = phaseStable
		return
	}

	st.lastReplicas = to
	st.phase = phaseCooldown
	st.cooldownUntil = time.Now().Add(time.Duration(rule.CooldownSeconds) * time.Second)
}

// serviceAvg computes the average metric value across all replicas of
// a service over the last 60 seconds, CPU in the given unit.
func (c *Controller) serviceAvg(ctx context.Context, stackName, service, metric, cpuUnit string) (float64, error) {
	if c.metrics == nil {
		return 0, fmt.Errorf("metrics collector unavailable")
	}
	// Containers are named <stack>-<service>-1, <stack>-<service>-2, ...
	// The metrics collector stores per-container samples keyed by name.
	// We query each and average.
	prefix := stackName + "-" + service + "-"
	now := time.Now()
	from := now.Add(-60 * time.Second)

	// Query all containers for this service by trying indices 1..20.
	// (The metrics table doesn't support LIKE queries, so we enumerate.)
	var total float64
	var count int
	for i := 1; i <= 20; i++ {
		name := fmt.Sprintf("%s%d", prefix, i)
		samples, err := c.metrics.Query(ctx, metrics.Query{
			Name: name,
			From: from,
			To:   now,
		})
		if err != nil || len(samples) == 0 {
			if i == 1 {
				// No samples at all — service might not be running.
				continue
			}
			break // No more replicas.
		}
		for _, s := range samples {
			switch strings.ToLower(metric) {
			case "cpu":
				total += cpuValue(s, cpuUnit)
			case "memory":
				if s.MemLimit > 0 {
					total += float64(s.MemUsed) / float64(s.MemLimit) * 100
				}
			}
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}
	return total / float64(count), nil
}

func cpuValue(s metrics.Sample, unit string) float64 {
	if unit == CPUUnitHost {
		return s.CPUPercent
	}
	return s.CPUPercentCore
}

// ReplicaCount returns the last known replica count for a service.
// Used by the rules API to report current state.
func (c *Controller) ReplicaCount(stackName, service string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := stackName + "/" + service
	if st, ok := c.states[key]; ok && st.lastReplicas > 0 {
		return st.lastReplicas
	}
	return 0
}

// UpdateReplicaCount is called after a manual scale so the controller
// knows the current state without waiting for a full tick cycle.
func (c *Controller) UpdateReplicaCount(stackName, service string, count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := stackName + "/" + service
	st, ok := c.states[key]
	if !ok {
		st = &ruleState{phase: phaseStable}
		c.states[key] = st
	}
	st.lastReplicas = count
	// Treat manual scale as a cooldown trigger so auto-scaler doesn't
	// immediately undo the user's action.
	st.phase = phaseCooldown
	st.cooldownUntil = time.Now().Add(60 * time.Second)
}
