// Package deploy holds the in-memory deploy-progress tracker that
// compose.Service and the auto-deploy callback update as they walk
// through a stack's services. The UI polls /stacks/{name}/deploy/progress
// to render a live phase + service + elapsed indicator instead of
// the static "deploying…" pill it had before.
//
// The tracker is intentionally tiny — a single map guarded by RWMutex.
// State lives for the duration of one deploy and is cleared shortly
// after Finish so the UI doesn't keep showing stale "done" forever.
package deploy

import (
	"sync"
	"time"
)

// Phase identifies what the executor is doing right now. The values
// match the UI's expected vocabulary — adding a new phase here means
// the frontend also needs to know how to render it.
type Phase string

const (
	PhaseStarting   Phase = "starting"   // executor warming up, no service yet
	PhasePulling    Phase = "pulling"    // currently pulling an image
	PhaseCreating   Phase = "creating"   // creating a container
	PhaseStarting2  Phase = "starting_container"
	PhaseHealthwait Phase = "healthcheck" // waiting on a container's healthcheck
	PhaseDone       Phase = "done"
	PhaseFailed     Phase = "failed"
)

// State is the snapshot the UI consumes per poll. Time fields are
// JSON-serialized as RFC3339 so the frontend can compute "elapsed"
// without server-side clock dependency.
type State struct {
	Stack     string    `json:"stack"`
	Phase     Phase     `json:"phase"`
	Service   string    `json:"service,omitempty"`
	Image     string    `json:"image,omitempty"`
	StepIndex int       `json:"step_index"`        // 1-based; 0 if pre-loop
	StepTotal int       `json:"step_total"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
}

// Tracker is a process-wide singleton. concurrency-safe.
type Tracker struct {
	mu     sync.RWMutex
	states map[string]*State
}

func NewTracker() *Tracker {
	return &Tracker{states: make(map[string]*State)}
}

// Begin starts tracking a fresh deploy. Clears any previous done/failed
// state for that stack so the UI doesn't show stale errors.
func (t *Tracker) Begin(stack string, totalServices int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.states[stack] = &State{
		Stack:     stack,
		Phase:     PhaseStarting,
		StepTotal: totalServices,
		StartedAt: now,
		UpdatedAt: now,
	}
}

// SetPhase moves the current deploy to a new phase. step is 1-based;
// pass 0 if the phase isn't service-scoped (e.g. very first state).
// Phase is a plain string so the compose package can pass its own
// constants without importing this package.
func (t *Tracker) SetPhase(stack string, phase string, service, image string, step int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.states[stack]
	if !ok {
		return
	}
	s.Phase = Phase(phase)
	s.Service = service
	s.Image = image
	if step > 0 {
		s.StepIndex = step
	}
	s.UpdatedAt = time.Now()
}

// Finish marks the deploy as done or failed. Schedules a delayed
// cleanup so the UI gets one last poll showing the terminal state
// before the state disappears — without that, the indicator would
// abruptly switch back to "no deploy in progress" the instant the
// executor returns and the user misses the resolution.
func (t *Tracker) Finish(stack string, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	s, ok := t.states[stack]
	if !ok {
		t.mu.Unlock()
		return
	}
	if err != nil {
		s.Phase = PhaseFailed
		s.Error = err.Error()
	} else {
		s.Phase = PhaseDone
	}
	s.UpdatedAt = time.Now()
	t.mu.Unlock()

	// Linger 30s so a polling UI is guaranteed to catch the terminal
	// state at its 1-2s refresh interval; then drop the entry.
	go func() {
		time.Sleep(30 * time.Second)
		t.mu.Lock()
		// Only delete if still the same state we just terminated —
		// a fresh Begin() could have replaced it.
		if cur, ok := t.states[stack]; ok && cur == s {
			delete(t.states, stack)
		}
		t.mu.Unlock()
	}()
}

// Get returns the live state for a stack, or nil when no deploy is
// (or recently was) in flight.
func (t *Tracker) Get(stack string) *State {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.states[stack]
	if !ok {
		return nil
	}
	// Return a copy so callers can't race against the next mutation.
	cp := *s
	return &cp
}
