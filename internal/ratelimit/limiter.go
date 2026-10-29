// Package ratelimit provides a minimal in-memory sliding-window limiter
// used for brute-force protection on auth endpoints (§1.5).
//
// The limiter is process-local: a restart resets all counters. For the
// Phase 1 single-binary target that's an acceptable tradeoff, and it keeps
// the dependency graph small (no Redis, no external KV store).
package ratelimit

import (
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
	// limiter is full even after dropping expired keys, Acquire refuses
	// new keys rather than forgetting tracked ones.
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
	if !ok || now.Sub(b.firstFailAt) > l.Window {
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

// Acquire counts an attempt before it is checked, so parallel requests
// can't all pass a check that only a later Fail would close. It refuses
// once MaxFailures attempts are counted in the window or the key is
// locked, and then returns how long to wait. Each granted attempt must be
// followed by Settle when it failed or Refund when it succeeded.
func (l *Limiter) Acquire(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if ok && now.Before(b.lockedUntil) {
		return false, b.lockedUntil.Sub(now)
	}
	if !ok || now.Sub(b.firstFailAt) >= l.Window {
		if !ok && l.MaxKeys > 0 && len(l.buckets) >= l.MaxKeys {
			if now.Sub(l.lastSweep) >= time.Minute {
				l.sweep(now)
				l.lastSweep = now
			}
			if len(l.buckets) >= l.MaxKeys {
				return false, l.Window
			}
		}
		b = &bucket{firstFailAt: now}
		l.buckets[key] = b
	}
	if b.failures >= l.MaxFailures {
		return false, b.firstFailAt.Add(l.Window).Sub(now)
	}
	b.failures++
	return true, 0
}

// Settle records that an acquired attempt failed. It locks the key for
// LockDuration once MaxFailures are counted and reports when this call
// started the lock.
func (l *Limiter) Settle(key string) (until time.Time, started bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok || now.Before(b.lockedUntil) || b.failures < l.MaxFailures {
		return time.Time{}, false
	}
	b.lockedUntil = now.Add(l.LockDuration)
	return b.lockedUntil, true
}

// Refund takes back an acquired attempt that succeeded, leaving the
// other counted failures in place.
func (l *Limiter) Refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[key]; ok && b.failures > 0 {
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
