package slack

import (
	"context"
	"sync"
	"time"
)

// A Client belongs to one Slack installation. Retry-After applies to a method
// across all its conversations, not just the request that received the 429.
// This coordinates callers in this process; it is not a distributed quota.
type methodCooldowns struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func (c *methodCooldowns) deferFor(method string, delay time.Duration) {
	deadline := time.Now().Add(delay)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.until == nil {
		c.until = make(map[string]time.Time)
	}
	if deadline.After(c.until[method]) {
		c.until[method] = deadline
	}
}

func (c *methodCooldowns) wait(ctx context.Context, method string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		delay := time.Until(c.until[method])
		if delay <= 0 {
			delete(c.until, method)
		}
		c.mu.Unlock()
		if delay <= 0 {
			return nil
		}
		if err := sleepSlackRetry(ctx, 0, delay); err != nil {
			return err
		}
		// Another request may have extended the method's cooldown while we waited.
	}
}
