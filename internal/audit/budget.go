package audit

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"
)

// budget lets each key write a burst of entries per window. Events
// beyond that are only counted into the window's tally T and written as
// one summary entry when the window is over.
type budget[T any] struct {
	window  time.Duration
	maxKeys int

	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*bucket[T]
}

// budgetKey is one budget an event draws from, with its burst.
type budgetKey struct {
	name  string
	burst int
}

type bucket[T any] struct {
	key        string
	start      time.Time
	end        time.Time
	written    int
	suppressed int
	tally      T
}

func (b *budget[T]) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// charge reports whether an event counted for all of keys is written in
// full, which needs room in the window of every key. Otherwise the event
// is counted once, by count on the first key whose window is full. The
// windows that ended before the event and still have events to summarise
// are returned too.
func (b *budget[T]) charge(keys []budgetKey, count func(key string, tally *T)) (bool, []bucket[T]) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	var out []bucket[T]
	for _, k := range keys {
		w := b.buckets[k.name]
		if w != nil && now.Sub(w.start) >= b.window {
			if w.suppressed > 0 {
				w.end = w.start.Add(b.window)
				out = append(out, *w)
			}
			delete(b.buckets, k.name)
		}
	}
	for _, k := range keys {
		if w := b.buckets[k.name]; w != nil && w.written >= k.burst {
			w.suppressed++
			count(k.name, &w.tally)
			return false, out
		}
	}
	for _, k := range keys {
		w := b.buckets[k.name]
		if w == nil {
			if b.buckets == nil {
				b.buckets = map[string]*bucket[T]{}
			}
			if len(b.buckets) >= b.maxKeys {
				out = append(out, b.expiredLocked(now, false)...)
			}
			if len(b.buckets) >= b.maxKeys {
				// Evicting ends the oldest window early: its summary is
				// written now, so no count is lost.
				out = append(out, b.evictOldestLocked(now)...)
			}
			w = &bucket[T]{key: k.name, start: now}
			b.buckets[k.name] = w
		}
		w.written++
	}
	return true, out
}

func (b *budget[T]) evictOldestLocked(now time.Time) []bucket[T] {
	var oldest *bucket[T]
	for _, w := range b.buckets {
		if oldest == nil || w.start.Before(oldest.start) {
			oldest = w
		}
	}
	if oldest == nil {
		return nil
	}
	delete(b.buckets, oldest.key)
	if oldest.suppressed == 0 {
		return nil
	}
	oldest.end = now
	return []bucket[T]{*oldest}
}

// expired returns the windows that are over, or every open window when
// all is set, that have events to summarise, and forgets them.
func (b *budget[T]) expired(all bool) []bucket[T] {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.expiredLocked(b.clock(), all)
}

func (b *budget[T]) expiredLocked(now time.Time, all bool) []bucket[T] {
	var out []bucket[T]
	for _, key := range slices.Sorted(maps.Keys(b.buckets)) {
		w := b.buckets[key]
		if !all && now.Sub(w.start) < b.window {
			continue
		}
		if w.suppressed > 0 {
			w.end = w.start.Add(b.window)
			if now.Before(w.end) {
				w.end = now
			}
			out = append(out, *w)
		}
		delete(b.buckets, key)
	}
	return out
}

// maxSummaryNames caps the distinct names a summary counts one by one.
const maxSummaryNames = 32

// countCapped adds one to m[name], counting names beyond maxSummaryNames
// distinct ones as "other".
func countCapped(m map[string]int, name string) map[string]int {
	if m == nil {
		m = map[string]int{}
	}
	if _, ok := m[name]; !ok && len(m) >= maxSummaryNames {
		name = "other"
	}
	m[name]++
	return m
}

// countTop adds one to m, which keeps the maxSummaryNames most frequent
// names (Space-Saving): a new name takes the place of the least counted
// one and inherits its count. Whoever floods a summary with names first
// can't push out a name that keeps coming; the counts add up to the
// events, but a name's count may include up to the smallest count of
// the names it replaced.
func countTop(m map[string]int, name string) map[string]int {
	if m == nil {
		m = map[string]int{}
	}
	if _, ok := m[name]; ok || len(m) < maxSummaryNames {
		m[name]++
		return m
	}
	minName, minCount := "", 0
	for n, c := range m {
		if minName == "" || c < minCount || (c == minCount && n < minName) {
			minName, minCount = n, c
		}
	}
	delete(m, minName)
	m[name] = minCount + 1
	return m
}

// writeSummary stores a summary entry. Its window is already forgotten,
// so a failed write is tried once more and then logged with the count.
func (s *Service) writeSummary(ctx context.Context, userID, action, target string, details any) {
	if s.WriteErr(ctx, userID, action, target, details) == nil {
		return
	}
	if err := s.WriteErr(ctx, userID, action, target, details); err != nil {
		slog.Error("audit summary lost", "action", action, "target", target, "err", err)
	}
}

// startFlusher calls flush every interval, and with all set once stop is
// called; stop returns when that last flush is done.
func startFlusher(ctx context.Context, interval time.Duration, flush func(ctx context.Context, all bool)) (stop func()) {
	ctx = context.WithoutCancel(ctx)
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-quit:
				flush(ctx, true)
				return
			case <-t.C:
				flush(ctx, false)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(quit) })
		<-done
	}
}
