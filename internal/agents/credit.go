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
	wake chan struct{}
}

func NewCredit(n int) *Credit {
	return &Credit{n: n, wake: make(chan struct{}, 1)}
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
	c.n += n
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// GrantBatch is how many consumed frames are acknowledged together, so a
// credit frame doesn't follow every single data frame.
const GrantBatch = StreamWindow / 4

// CreditCount decodes the count of a StreamOpCredit control frame.
func CreditCount(params map[string]any) int {
	switch n := params["n"].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
