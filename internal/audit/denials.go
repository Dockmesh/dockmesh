package audit

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// Limits for auth.scope_denied entries, so a caller looping a refused
// request can't grow the log without bound or push other events out of
// the webhook queue.
const (
	DenialWindow       = time.Minute
	DenialBurst        = 10
	maxDenialActors    = 4096
	maxDenialPermNames = 32
)

// DenialActor identifies who a refusal is counted for. Key separates the
// budgets: a user's sessions and each of its API tokens count apart.
type DenialActor struct {
	Key     string
	UserID  string
	TokenID int64
}

// denialBucket counts the refusals of one actor in the current window.
// The first DenialBurst are written as usual; the rest are only counted
// and written as one summary entry when the window is over.
type denialBucket struct {
	actor      DenialActor
	start      time.Time
	written    int
	suppressed int
	perms      map[string]int
}

type denials struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*denialBucket
}

// WriteDenied records an auth.scope_denied entry for actor, unless actor
// already had DenialBurst entries in the current window; then the refusal
// is counted for the window's summary entry, which names the refused
// permissions. Entries are written even if the request is cancelled
// meanwhile, so a dropped connection can't discard a window's count.
func (s *Service) WriteDenied(ctx context.Context, actor DenialActor, perm, target string, details any) {
	ctx = context.WithoutCancel(ctx)
	write, summaries := s.denials.admit(actor, perm)
	s.writeDenialSummaries(ctx, summaries)
	if write {
		s.Write(ctx, actor.UserID, ActionScopeDenied, target, details)
	}
}

// flushDenials writes the summary entries of every window that is over,
// or of every open window when all is set.
func (s *Service) flushDenials(ctx context.Context, all bool) {
	s.writeDenialSummaries(ctx, s.denials.expired(all))
}

// StartDenialFlusher writes finished summaries every DenialWindow, so the
// count of a burst is logged even when no further refusal follows. The
// returned stop writes the open windows and returns once they are
// stored; call it before the webhook and the database are closed.
func (s *Service) StartDenialFlusher(ctx context.Context) (stop func()) {
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(DenialWindow)
		defer t.Stop()
		for {
			select {
			case <-quit:
				s.flushDenials(context.WithoutCancel(ctx), true)
				return
			case <-t.C:
				s.flushDenials(context.WithoutCancel(ctx), false)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(quit) })
		<-done
	}
}

func (s *Service) writeDenialSummaries(ctx context.Context, summaries []denialBucket) {
	for _, b := range summaries {
		details := map[string]any{
			"suppressed":   b.suppressed,
			"window_start": b.start.UTC().Format(time.RFC3339),
			"window_end":   b.start.Add(DenialWindow).UTC().Format(time.RFC3339),
			"permissions":  b.perms,
		}
		if b.actor.TokenID != 0 {
			details["api_token_id"] = b.actor.TokenID
		}
		if b.actor.UserID == "" {
			details["actor"] = b.actor.Key
		}
		s.Write(ctx, b.actor.UserID, ActionScopeDenied, fmt.Sprintf("%d more refused requests", b.suppressed), details)
	}
}

func (d *denials) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// admit reports whether this refusal is written in full, and returns the
// windows that ended before it with refusals still to summarise.
func (d *denials) admit(actor DenialActor, perm string) (bool, []denialBucket) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.clock()
	var out []denialBucket
	b := d.buckets[actor.Key]
	if b != nil && now.Sub(b.start) >= DenialWindow {
		if b.suppressed > 0 {
			out = append(out, *b)
		}
		delete(d.buckets, actor.Key)
		b = nil
	}
	if b == nil {
		if d.buckets == nil {
			d.buckets = map[string]*denialBucket{}
		}
		if len(d.buckets) >= maxDenialActors {
			out = append(out, d.expiredLocked(now, false)...)
		}
		if len(d.buckets) >= maxDenialActors {
			// Evicting ends the oldest window early: its summary is written
			// now, so no count is lost and every entry keeps its actor.
			out = append(out, d.evictOldestLocked()...)
		}
		b = &denialBucket{actor: actor, start: now}
		d.buckets[actor.Key] = b
	}
	if b.written < DenialBurst {
		b.written++
		return true, out
	}
	b.suppressed++
	if b.perms == nil {
		b.perms = map[string]int{}
	}
	if _, ok := b.perms[perm]; !ok && len(b.perms) >= maxDenialPermNames {
		perm = "other"
	}
	b.perms[perm]++
	return false, out
}

func (d *denials) evictOldestLocked() []denialBucket {
	var oldest *denialBucket
	for _, b := range d.buckets {
		if oldest == nil || b.start.Before(oldest.start) {
			oldest = b
		}
	}
	if oldest == nil {
		return nil
	}
	delete(d.buckets, oldest.actor.Key)
	if oldest.suppressed == 0 {
		return nil
	}
	return []denialBucket{*oldest}
}

func (d *denials) expired(all bool) []denialBucket {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.expiredLocked(d.clock(), all)
}

func (d *denials) expiredLocked(now time.Time, all bool) []denialBucket {
	var out []denialBucket
	for _, key := range slices.Sorted(maps.Keys(d.buckets)) {
		b := d.buckets[key]
		if !all && now.Sub(b.start) < DenialWindow {
			continue
		}
		if b.suppressed > 0 {
			out = append(out, *b)
		}
		delete(d.buckets, key)
	}
	return out
}
