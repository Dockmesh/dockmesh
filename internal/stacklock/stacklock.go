// Package stacklock holds one lock per stack and target, shared by every
// operation that recreates a stack's containers, so two of them never fight
// over the same names and replicas. A maintenance lock takes all stacks of a
// set of targets at once.
package stacklock

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// TargetKind says what a stack is deployed to.
type TargetKind string

const KindHost TargetKind = "host"

// Target is what a stack runs on.
type Target struct {
	Kind TargetKind
	ID   string
}

// Host returns the target for a host id; "" is the local host.
func Host(id string) Target {
	if id == "" {
		id = "local"
	}
	return Target{Kind: KindHost, ID: id}
}

// Key identifies one stack on one target.
type Key struct {
	Target Target
	Stack  string
}

// HostKey is the key of stack on host id.
func HostKey(id, stack string) Key { return Key{Target: Host(id), Stack: stack} }

// Operation names shown to whoever finds a stack busy.
const (
	OpDeploy      = "deploy"
	OpScale       = "scale"
	OpRolling     = "rolling update"
	OpImageUpdate = "image update"
	OpRollback    = "image rollback"
	OpGitDeploy   = "git auto-deploy"
	OpAutoScale   = "auto-scaling"
	OpMaintenance = "maintenance"
	// OpDependency leaves out which stack needs the dependency: the caller
	// who finds it busy may have no access to that stack.
	OpDependency = "dependency deploy"
)

// ErrBusy matches every BusyError.
var ErrBusy = errors.New("stack is busy")

// BusyError reports the operation that holds a stack.
type BusyError struct {
	Stack     string
	Operation string
	Since     time.Time
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("stack %s is busy: %s running since %s", e.Stack, e.Operation, e.Since.UTC().Format(time.RFC3339))
}

func (e *BusyError) Is(target error) bool { return target == ErrBusy }

type holder struct {
	op    string
	since time.Time
}

// Locks is safe for concurrent use. A nil *Locks locks nothing.
type Locks struct {
	mu      sync.Mutex
	held    map[Key]holder
	maint   map[Target]holder
	changed chan struct{}
	now     func() time.Time
}

func New() *Locks {
	return &Locks{
		held:    make(map[Key]holder),
		maint:   make(map[Target]holder),
		changed: make(chan struct{}),
		now:     time.Now,
	}
}

func noop() {}

// TryLock takes the lock for key or returns a *BusyError naming the
// operation that holds it. The returned release may be called more than once.
func (l *Locks) TryLock(key Key, op string) (func(), error) {
	if l == nil {
		return noop, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tryLocked(key, op)
}

func (l *Locks) tryLocked(key Key, op string) (func(), error) {
	if h, ok := l.maint[key.Target]; ok {
		return nil, &BusyError{Stack: key.Stack, Operation: h.op, Since: h.since}
	}
	if h, ok := l.held[key]; ok {
		return nil, &BusyError{Stack: key.Stack, Operation: h.op, Since: h.since}
	}
	l.held[key] = holder{op: op, since: l.now()}
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.held, key)
			l.broadcastLocked()
			l.mu.Unlock()
		})
	}, nil
}

// Lock waits until key is free and takes it. When ctx ends first it returns
// the *BusyError of the operation that still holds the stack.
func (l *Locks) Lock(ctx context.Context, key Key, op string) (func(), error) {
	if l == nil {
		return noop, nil
	}
	for {
		l.mu.Lock()
		release, err := l.tryLocked(key, op)
		wait := l.changed
		l.mu.Unlock()
		if err == nil {
			return release, nil
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (%w)", err, ctx.Err())
		}
	}
}

// Maintenance takes every stack lock of targets: from the call on, new locks
// on them are refused, and it waits up to timeout for the running ones to
// finish. On timeout it gives the targets back and names the stacks still
// busy.
func (l *Locks) Maintenance(ctx context.Context, targets []Target, op string, timeout time.Duration) (func(), error) {
	if l == nil {
		return noop, nil
	}
	targets = slices.Clone(targets)
	l.mu.Lock()
	for _, t := range targets {
		if h, ok := l.maint[t]; ok {
			l.mu.Unlock()
			return nil, fmt.Errorf("%w: %s of %s running since %s", ErrBusy, h.op, t.ID, h.since.UTC().Format(time.RFC3339))
		}
	}
	since := l.now()
	for _, t := range targets {
		l.maint[t] = holder{op: op, since: since}
	}
	l.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			l.mu.Lock()
			for _, t := range targets {
				delete(l.maint, t)
			}
			l.broadcastLocked()
			l.mu.Unlock()
		})
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		l.mu.Lock()
		busy := l.busyOnLocked(targets)
		wait := l.changed
		l.mu.Unlock()
		if len(busy) == 0 {
			return release, nil
		}
		select {
		case <-wait:
		case <-ctx.Done():
			release()
			return nil, fmt.Errorf("%w: still running: %s (%w)", ErrBusy, strings.Join(busy, ", "), ctx.Err())
		}
	}
}

func (l *Locks) busyOnLocked(targets []Target) []string {
	var busy []string
	for k, h := range l.held {
		if slices.Contains(targets, k.Target) {
			busy = append(busy, k.Stack+" ("+h.op+")")
		}
	}
	slices.Sort(busy)
	return busy
}

func (l *Locks) broadcastLocked() {
	close(l.changed)
	l.changed = make(chan struct{})
}
