package agents

import (
	"context"
	"sync"
)

// Credit counts how many stream data frames a producer may still send
// before the consumer grants more. One producer goroutine per Credit.
type Credit struct {
	mu   sync.Mutex
	n    int
	max  int
	wake chan struct{}
}

// NewCredit starts with n credits; grants never raise the balance above n.
func NewCredit(n int) *Credit {
	return &Credit{n: n, max: n, wake: make(chan struct{}, 1)}
}

// Acquire takes one credit, waiting until one is granted, ctx is done or
// stop is closed. A nil stop never fires.
func (c *Credit) Acquire(ctx context.Context, stop <-chan struct{}) error {
	for {
		c.mu.Lock()
		if c.n > 0 {
			c.n--
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		select {
		case <-c.wake:
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			return ErrStreamClosed
		}
	}
}

// Add grants n more credits.
func (c *Credit) Add(n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.n = min(c.n+n, c.max)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// GrantBatch is how many consumed frames are acknowledged together, so a
// credit frame doesn't follow every single data frame.
const GrantBatch = StreamWindow / 4

// CreditCount decodes the count of a StreamOpCredit control frame (or a
// window); anything outside 1..StreamWindow counts as 0.
func CreditCount(params map[string]any) int {
	var n float64
	switch v := params["n"].(type) {
	case float64:
		n = v
	case int:
		n = float64(v)
	default:
		return 0
	}
	if n < 1 || n > StreamWindow {
		return 0
	}
	return int(n)
}
