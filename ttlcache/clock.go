package ttlcache

import (
	"sync"
	"time"
)

// Clock supplies time to the cache. Production callers normally use the
// default real clock; tests can use ManualClock without sleeping.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// ManualClock is a concurrency-safe controllable clock intended for tests.
type ManualClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewManualClock creates a clock at now.
func NewManualClock(now time.Time) *ManualClock { return &ManualClock{now: now} }

// Now returns the clock's current time.
func (c *ManualClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

// Advance moves the clock forward by d. A negative duration moves it back.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
