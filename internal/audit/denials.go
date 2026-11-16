package audit

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// Limits for auth.scope_denied entries. Every refused request used to
// write one chained row and one webhook delivery, so a caller looping a
// refused request could grow the log without bound and push real events
// out of the webhook queue.
const (
	DenialWindow       = time.Minute
	DenialBurst        = 10
	maxDenialActors    = 4096
	maxDenialPermNames = 32
)

// denialBucket counts the refusals of one actor in the current window.
// The first DenialBurst are written as usual; the rest are only counted
// and written as one summary entry when the window is over.
type denialBucket struct {
	start      time.Time
	written    int
	suppressed int
	perms      map[string]int
	userID     string
}

type denials struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*denialBucket
}

// denialSummary is a finished window with refusals that were not written.
type denialSummary struct {
	actor  string
	userID string
	bucket denialBucket
}

// WriteDenied records an auth.scope_denied entry for actor (the user id,
// or "ip:<addr>" without one), unless actor already had DenialBurst
// entries in the current window; then the refusal is counted for the
// window's summary entry. perm names the refused permission in that
// summary.
func (s *Service) WriteDenied(ctx context.Context, actor, userID, perm, target string, details any) {
	write, summaries := s.denials.admit(actor, userID, perm)
	s.writeDenialSummaries(ctx, summaries)
	if write {
		s.Write(ctx, userID, ActionScopeDenied, target, details)
	}
}

// FlushDenials writes the summary entries of every window that is over.
func (s *Service) FlushDenials(ctx context.Context) {
	s.writeDenialSummaries(ctx, s.denials.expired(false))
}

// StartDenialFlusher writes finished summaries every DenialWindow, so the
// count of a burst is logged even when no further refusal follows, and
// writes the open ones when ctx ends.
func (s *Service) StartDenialFlusher(ctx context.Context) {
	go func() {
		t := time.NewTicker(DenialWindow)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.writeDenialSummaries(context.WithoutCancel(ctx), s.denials.expired(true))
				return
			case <-t.C:
				s.FlushDenials(ctx)
			}
		}
	}()
}

func (s *Service) writeDenialSummaries(ctx context.Context, summaries []denialSummary) {
	for _, sum := range summaries {
		b := sum.bucket
		details := map[string]any{
			"suppressed":   b.suppressed,
			"window_start": b.start.UTC().Format(time.RFC3339),
			"window_end":   b.start.Add(DenialWindow).UTC().Format(time.RFC3339),
			"permissions":  b.perms,
		}
		if sum.userID == "" {
			details["actor"] = sum.actor
		}
		s.Write(ctx, sum.userID, ActionScopeDenied, fmt.Sprintf("%d more refused requests", b.suppressed), details)
	}
}

func (d *denials) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// admit reports whether this refusal is written in full, and returns the
// summaries of windows that ended before it.
func (d *denials) admit(actor, userID, perm string) (bool, []denialSummary) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.clock()
	var out []denialSummary
	b := d.buckets[actor]
	if b != nil && now.Sub(b.start) >= DenialWindow {
		if b.suppressed > 0 {
			out = append(out, denialSummary{actor: actor, userID: b.userID, bucket: *b})
		}
		delete(d.buckets, actor)
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
			// Every slot holds a live window: count this refusal in a
			// shared bucket rather than letting distinct actors evict
			// each other's counts.
			actor, userID = "overflow", ""
			b = d.buckets[actor]
		}
		if b == nil {
			b = &denialBucket{start: now, userID: userID}
			d.buckets[actor] = b
		}
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

func (d *denials) expired(all bool) []denialSummary {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.expiredLocked(d.clock(), all)
}

func (d *denials) expiredLocked(now time.Time, all bool) []denialSummary {
	var out []denialSummary
	for _, actor := range slices.Sorted(maps.Keys(d.buckets)) {
		b := d.buckets[actor]
		if !all && now.Sub(b.start) < DenialWindow {
			continue
		}
		if b.suppressed > 0 {
			out = append(out, denialSummary{actor: actor, userID: b.userID, bucket: *b})
		}
		delete(d.buckets, actor)
	}
	return out
}
