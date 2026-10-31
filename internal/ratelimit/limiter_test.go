package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	// fakeNow lets us control time without sleeping.
	now := time.Unix(0, 0)
	l := &Limiter{
		MaxFailures:  3,
		Window:       time.Minute,
		LockDuration: 5 * time.Minute,
		buckets:      make(map[string]*bucket),
		now:          func() time.Time { return now },
	}

	ok, _ := l.Check("ip1")
	if !ok {
		t.Fatal("fresh key should be allowed")
	}

	l.Fail("ip1")
	l.Fail("ip1")
	if ok, _ := l.Check("ip1"); !ok {
		t.Fatal("2 failures below threshold should still allow")
	}

	l.Fail("ip1") // 3rd failure → lock
	ok, retry := l.Check("ip1")
	if ok {
		t.Fatal("expected lockout after 3 failures")
	}
	if retry != 5*time.Minute {
		t.Errorf("retry = %v, want 5m", retry)
	}

	// Other key unaffected.
	if ok, _ := l.Check("ip2"); !ok {
		t.Error("ip2 should still be allowed")
	}

	// Advance past lock.
	now = now.Add(6 * time.Minute)
	if ok, _ := l.Check("ip1"); !ok {
		t.Error("should be allowed again after lock expires")
	}

	// Success clears bucket.
	l.Fail("ip3")
	l.Fail("ip3")
	l.Succeed("ip3")
	l.Fail("ip3")
	l.Fail("ip3")
	if ok, _ := l.Check("ip3"); !ok {
		t.Error("success should have cleared the counter")
	}
}

func TestLimiter_WindowExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	l := &Limiter{
		MaxFailures:  3,
		Window:       time.Minute,
		LockDuration: 5 * time.Minute,
		buckets:      make(map[string]*bucket),
		now:          func() time.Time { return now },
	}

	l.Fail("ip1")
	l.Fail("ip1")

	// Advance past the failure window — counter should reset on next fail.
	now = now.Add(2 * time.Minute)
	l.Fail("ip1")
	l.Fail("ip1")
	if ok, _ := l.Check("ip1"); !ok {
		t.Error("expired window should not lock (only 2 fails in current window)")
	}
}

func TestLimiterAcquire(t *testing.T) {
	type step struct {
		op   string // acquire, settle, refund, advance, len
		key  string
		d    time.Duration
		want bool // acquire: granted; settle: lock started
		n    int  // len: tracked buckets
	}
	tests := []struct {
		name    string
		maxKeys int
		steps   []step
	}{
		{name: "counts before the check", steps: []step{
			{op: "acquire", key: "a", want: true},
			{op: "acquire", key: "a", want: true},
			{op: "acquire", key: "a", want: false}, // in flight: both slots taken
			{op: "settle", key: "a", want: true},
			{op: "settle", key: "a", want: false}, // already locked
			{op: "acquire", key: "a", want: false},
			{op: "advance", d: 2 * time.Minute},
			{op: "acquire", key: "a", want: true},
		}},
		{name: "refund frees the slot", steps: []step{
			{op: "acquire", key: "a", want: true},
			{op: "acquire", key: "a", want: true},
			{op: "refund", key: "a"},
			{op: "settle", key: "a", want: false},
			{op: "acquire", key: "a", want: true},
		}},
		{name: "refund after a window reset is ignored", steps: []step{
			{op: "acquire", key: "a", want: true}, // ticket from the old window
			{op: "advance", d: 2 * time.Minute},
			{op: "acquire", key: "b", want: true},
			{op: "acquire", key: "a", want: true}, // new window, but keep the old ticket
			{op: "refund-old", key: "a"},
			{op: "acquire", key: "a", want: true},
			{op: "acquire", key: "a", want: false}, // the old refund didn't free a slot
		}},
		{name: "full limiter shares overflow buckets", maxKeys: 1, steps: []step{
			{op: "acquire", key: "a", want: true},
			{op: "acquire", key: "b", want: true},
			{op: "acquire", key: "b", want: true},
			{op: "settle", key: "b", want: true},
			{op: "acquire", key: "b", want: false}, // overflow bucket locked
			{op: "len", n: 2},
			{op: "advance", d: 2 * time.Minute},
			{op: "acquire", key: "c", want: true}, // expired buckets swept, "c" tracked on its own
			{op: "len", n: 1},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(0, 0)
			l := &Limiter{
				MaxFailures:  2,
				Window:       time.Minute,
				LockDuration: time.Minute,
				MaxKeys:      tt.maxKeys,
				buckets:      make(map[string]*bucket),
				now:          func() time.Time { return now },
			}
			tickets := map[string][]Ticket{}
			for i, s := range tt.steps {
				switch s.op {
				case "acquire":
					tk, ok, _ := l.Acquire(s.key)
					if ok != s.want {
						t.Fatalf("step %d: acquire = %v, want %v", i, ok, s.want)
					}
					if ok {
						tickets[s.key] = append(tickets[s.key], tk)
					}
				case "settle":
					tk := tickets[s.key][len(tickets[s.key])-1]
					if _, started := l.Settle(tk); started != s.want {
						t.Fatalf("step %d: settle started = %v, want %v", i, started, s.want)
					}
				case "refund":
					l.Refund(tickets[s.key][len(tickets[s.key])-1])
				case "refund-old":
					l.Refund(tickets[s.key][0])
				case "advance":
					now = now.Add(s.d)
				case "len":
					if len(l.buckets) != s.n {
						t.Fatalf("step %d: %d buckets, want %d", i, len(l.buckets), s.n)
					}
				}
			}
		})
	}
}

func TestLimiterFailKeepsLock(t *testing.T) {
	now := time.Unix(0, 0)
	l := &Limiter{
		MaxFailures:  2,
		Window:       time.Minute,
		LockDuration: 5 * time.Minute,
		buckets:      make(map[string]*bucket),
		now:          func() time.Time { return now },
	}
	l.Fail("k")
	l.Fail("k")
	now = now.Add(2 * time.Minute) // window over, lock still running
	l.Fail("k")
	if ok, _ := l.Check("k"); ok {
		t.Fatal("a failure after the window reset a running lock")
	}
}
