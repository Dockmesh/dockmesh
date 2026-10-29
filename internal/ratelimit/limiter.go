// Package ratelimit provides a minimal in-memory sliding-window limiter
// used for brute-force protection on auth endpoints (§1.5).
//
// The limiter is process-local: a restart resets all counters. For the
// Phase 1 single-binary target that's an acceptable tradeoff, and it keeps
// the dependency graph small (no Redis, no external KV store).
package ratelimit

import (
	"hash/fnv"
	"strconv"
	"sync"
	"time"
)

// Limiter tracks failed attempts per key. After MaxFailures within Window,
// the key is locked for LockDuration. Successful attempts clear the bucket.
type Limiter struct {
	MaxFailures  int
	Window       time.Duration
	LockDuration time.Duration
	// MaxKeys caps the number of tracked keys; 0 means no cap. When the
	// limiter is full even after dropping expired keys, Acquire counts new
	// keys in one of overflowBuckets shared buckets with the same limits,
	// so a full limiter neither forgets keys nor treats new ones apart.
	MaxKeys int

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
	now       func() time.Time // overridable for tests
}

type bucket struct {
	failures    int
	firstFailAt time.Time
	lockedUntil time.Time
}

// New creates a limiter with the given thresholds and starts a janitor.
func New(maxFailures int, window, lockDuration time.Duration) *Limiter {
	l := &Limiter{
		MaxFailures:  maxFailures,
		Window:       window,
		LockDuration: lockDuration,
		buckets:      make(map[string]*bucket),
		now:          time.Now,
	}
	go l.janitor()
	return l
}

// Check returns (allowed, retryAfter). If allowed is false, retryAfter is
// the remaining lock duration.
func (l *Limiter) Check(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		return true, 0
	}
	now := l.now()
	if now.Before(b.lockedUntil) {
		return false, b.lockedUntil.Sub(now)
	}
	return true, 0
}

// Fail records a failed attempt. If the threshold is reached, the key is
// locked for LockDuration.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok || (now.Sub(b.firstFailAt) > l.Window && !now.Before(b.lockedUntil)) {
		l.buckets[key] = &bucket{failures: 1, firstFailAt: now}
		return
	}
	b.failures++
	if b.failures >= l.MaxFailures {
		b.lockedUntil = now.Add(l.LockDuration)
	}
}

// Succeed clears the bucket for a key.
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

const overflowBuckets = 4096

// Ticket identifies the bucket and window an acquired attempt was counted
// in, for the Settle or Refund that follows it.
type Ticket struct {
	key   string
	since time.Time
}

// Acquire counts an attempt before it is checked, so parallel requests
// can't all pass a check that only a later Fail would close. It refuses
// once MaxFailures attempts are counted in the window or the key is
// locked, and then returns how long to wait. Each granted attempt must be
// followed by Settle when it failed or Refund when it succeeded.
func (l *Limiter) Acquire(key string) (Ticket, bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok && l.MaxKeys > 0 && len(l.buckets) >= l.MaxKeys {
		if now.Sub(l.lastSweep) >= time.Minute {
			l.sweep(now)
			l.lastSweep = now
		}
		if len(l.buckets) >= l.MaxKeys {
			key = overflowKey(key)
			b, ok = l.buckets[key]
		}
	}
	if ok && now.Before(b.lockedUntil) {
		return Ticket{}, false, b.lockedUntil.Sub(now)
	}
	if !ok || now.Sub(b.firstFailAt) >= l.Window {
		b = &bucket{firstFailAt: now}
		l.buckets[key] = b
	}
	if b.failures >= l.MaxFailures {
		return Ticket{}, false, b.firstFailAt.Add(l.Window).Sub(now)
	}
	b.failures++
	return Ticket{key: key, since: b.firstFailAt}, true, 0
}

// overflowKey maps a key to a shared bucket. The NUL prefix keeps it apart
// from caller keys.
func overflowKey(key string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return "\x00overflow:" + strconv.Itoa(int(h.Sum32()%overflowBuckets))
}

// Settle records that an acquired attempt failed. It locks the bucket for
// LockDuration once MaxFailures are counted and reports when this call
// started the lock.
func (l *Limiter) Settle(t Ticket) (until time.Time, started bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[t.key]
	if !ok || now.Before(b.lockedUntil) || b.failures < l.MaxFailures {
		return time.Time{}, false
	}
	b.lockedUntil = now.Add(l.LockDuration)
	return b.lockedUntil, true
}

// Refund takes back an acquired attempt that succeeded, leaving the
// other counted failures in place. It does nothing once the window the
// attempt was counted in has been replaced.
func (l *Limiter) Refund(t Ticket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[t.key]; ok && b.firstFailAt.Equal(t.since) && b.failures > 0 {
		b.failures--
	}
}

func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.After(b.lockedUntil) && now.Sub(b.firstFailAt) > l.Window {
			delete(l.buckets, k)
		}
	}
}

func (l *Limiter) janitor() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		l.sweep(l.now())
		l.mu.Unlock()
	}
}
