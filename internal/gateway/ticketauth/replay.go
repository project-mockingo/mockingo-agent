package ticketauth

import (
	"sync"
	"time"
)

// ReplayCache is process-local. Entries live through ticket expiry plus skew;
// a process restart may forget consumed short-lived tickets.
type ReplayCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
	max     int
	skew    time.Duration
}

func NewReplayCache(max int, skew time.Duration) *ReplayCache {
	return &ReplayCache{entries: make(map[string]time.Time), max: max, skew: skew}
}

func (c *ReplayCache) Consume(id string, expiresAt time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.cleanupLocked(now)
	if deadline, found := c.entries[id]; found && now.Before(deadline) {
		return ErrReplay
	}
	if len(c.entries) >= c.max {
		return ErrReplayCacheFull
	}
	c.entries[id] = expiresAt.Add(c.skew)
	return nil
}

func (c *ReplayCache) Cleanup(now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cleanupLocked(now)
}

func (c *ReplayCache) cleanupLocked(now time.Time) int {
	removed := 0
	for id, deadline := range c.entries {
		if !now.Before(deadline) {
			delete(c.entries, id)
			removed++
		}
	}
	return removed
}
