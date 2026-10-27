package ttlcache

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

type entry[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
	cost      int64
	lru       *list.Element
}

type counters struct {
	hits, misses, sets, deletes, expirations, evictions, rejections atomic.Uint64
}

// Memory is a concurrent per-key TTL cache with optional LRU capacity limits.
type Memory[K comparable, V any] struct {
	mu      sync.Mutex
	cfg     Config[K, V]
	items   map[K]*entry[K, V]
	lru     list.List
	cost    int64
	closed  bool
	stop    chan struct{}
	done    chan struct{}
	close   sync.Once
	metrics counters
}

type eviction[K comparable, V any] struct {
	key    K
	value  V
	reason EvictionReason
}

// New creates a Memory cache. Call Close when CleanupInterval is positive.
func New[K comparable, V any](cfg Config[K, V]) *Memory[K, V] {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.CleanupInterval < 0 {
		cfg.CleanupInterval = 0
	}
	c := &Memory[K, V]{cfg: cfg, items: make(map[K]*entry[K, V]), stop: make(chan struct{}), done: make(chan struct{})}
	if cfg.CleanupInterval > 0 {
		go c.cleanupLoop()
	} else {
		close(c.done)
	}
	return c
}

func (c *Memory[K, V]) effectiveTTL(ttl time.Duration) (time.Duration, error) {
	if ttl == NoExpiration {
		return ttl, nil
	}
	if ttl < 0 {
		return 0, ErrInvalidTTL
	}
	if ttl == 0 {
		ttl = c.cfg.DefaultTTL
	}
	if ttl == NoExpiration {
		return ttl, nil
	}
	if ttl <= 0 {
		return 0, ErrInvalidTTL
	}
	return ttl, nil
}

func (c *Memory[K, V]) itemCost(key K, value V) (int64, error) {
	if c.cfg.Cost == nil {
		return 1, nil
	}
	n := c.cfg.Cost(key, value)
	if n < 0 {
		return 0, ErrInvalidCost
	}
	return n, nil
}

// Get returns a live value and updates its LRU position.
func (c *Memory[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	var zero V
	if c.closed {
		c.mu.Unlock()
		c.metrics.misses.Add(1)
		return zero, false
	}
	e, ok := c.items[key]
	if !ok {
		c.mu.Unlock()
		c.metrics.misses.Add(1)
		return zero, false
	}
	if c.expired(e, c.cfg.Clock.Now()) {
		ev := c.removeLocked(e, EvictedExpired)
		c.mu.Unlock()
		c.metrics.misses.Add(1)
		c.metrics.expirations.Add(1)
		c.notify(ev)
		return zero, false
	}
	c.lru.MoveToFront(e.lru)
	value := e.value
	c.mu.Unlock()
	c.metrics.hits.Add(1)
	return value, true
}

// Set stores value for ttl. A zero ttl uses Config.DefaultTTL; NoExpiration
// keeps the entry until it is deleted, replaced, evicted or cleared.
func (c *Memory[K, V]) Set(key K, value V, ttl time.Duration) error {
	ttl, err := c.effectiveTTL(ttl)
	if err != nil {
		c.metrics.rejections.Add(1)
		return err
	}
	cost, err := c.itemCost(key, value)
	if err != nil {
		c.metrics.rejections.Add(1)
		return err
	}
	if (c.cfg.MaxItemCost > 0 && cost > c.cfg.MaxItemCost) || (c.cfg.MaxCost > 0 && cost > c.cfg.MaxCost) {
		c.metrics.rejections.Add(1)
		return ErrItemTooLarge
	}
	now := c.cfg.Clock.Now()
	var expires time.Time
	if ttl != NoExpiration {
		expires = now.Add(ttl)
	}
	var events []eviction[K, V]
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.metrics.rejections.Add(1)
		return ErrClosed
	}
	if old := c.items[key]; old != nil {
		events = append(events, c.removeLocked(old, EvictedReplaced))
	}
	e := &entry[K, V]{key: key, value: value, expiresAt: expires, cost: cost}
	e.lru = c.lru.PushFront(e)
	c.items[key] = e
	c.cost += cost
	for c.overCapacityLocked() {
		back := c.lru.Back()
		if back == nil {
			break
		}
		events = append(events, c.removeLocked(back.Value.(*entry[K, V]), EvictedCapacity))
		c.metrics.evictions.Add(1)
	}
	c.mu.Unlock()
	c.metrics.sets.Add(1)
	c.notify(events...)
	return nil
}

// Delete removes key and reports whether it existed.
func (c *Memory[K, V]) Delete(key K) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	e := c.items[key]
	if e == nil {
		c.mu.Unlock()
		return false
	}
	ev := c.removeLocked(e, EvictedDeleted)
	c.mu.Unlock()
	c.metrics.deletes.Add(1)
	c.notify(ev)
	return true
}

// Expire changes an existing entry's TTL and reports whether it was updated.
func (c *Memory[K, V]) Expire(key K, ttl time.Duration) bool {
	ttl, err := c.effectiveTTL(ttl)
	if err != nil {
		return false
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	e := c.items[key]
	if e == nil {
		c.mu.Unlock()
		return false
	}
	if c.expired(e, c.cfg.Clock.Now()) {
		ev := c.removeLocked(e, EvictedExpired)
		c.mu.Unlock()
		c.metrics.expirations.Add(1)
		c.notify(ev)
		return false
	}
	if ttl == NoExpiration {
		e.expiresAt = time.Time{}
	} else {
		e.expiresAt = c.cfg.Clock.Now().Add(ttl)
	}
	c.mu.Unlock()
	return true
}

// TTL returns the remaining lifetime. It returns NoExpiration for a persistent
// entry and false for a missing or expired key.
func (c *Memory[K, V]) TTL(key K) (time.Duration, bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, false
	}
	e := c.items[key]
	if e == nil {
		c.mu.Unlock()
		return 0, false
	}
	if e.expiresAt.IsZero() {
		c.mu.Unlock()
		return NoExpiration, true
	}
	remaining := e.expiresAt.Sub(c.cfg.Clock.Now())
	if remaining <= 0 {
		ev := c.removeLocked(e, EvictedExpired)
		c.mu.Unlock()
		c.metrics.expirations.Add(1)
		c.notify(ev)
		return 0, false
	}
	c.mu.Unlock()
	return remaining, true
}

// Len returns the number of live entries after removing expired entries.
func (c *Memory[K, V]) Len() int {
	c.notify(c.cleanupExpired()...)
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Clear removes every entry.
func (c *Memory[K, V]) Clear() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	events := make([]eviction[K, V], 0, len(c.items))
	for _, e := range c.items {
		events = append(events, eviction[K, V]{e.key, e.value, EvictedCleared})
	}
	c.items = make(map[K]*entry[K, V])
	c.lru.Init()
	c.cost = 0
	c.mu.Unlock()
	c.notify(events...)
}

// Stats returns cumulative counters and the current entry count and cost.
func (c *Memory[K, V]) Stats() Stats {
	c.notify(c.cleanupExpired()...)
	c.mu.Lock()
	entries, cost := len(c.items), c.cost
	c.mu.Unlock()
	return Stats{c.metrics.hits.Load(), c.metrics.misses.Load(), c.metrics.sets.Load(), c.metrics.deletes.Load(), c.metrics.expirations.Load(), c.metrics.evictions.Load(), c.metrics.rejections.Load(), entries, cost}
}

// Close stops background cleanup. It is safe to call more than once.
func (c *Memory[K, V]) Close() error {
	c.close.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		if c.cfg.CleanupInterval > 0 {
			close(c.stop)
			<-c.done
		}
	})
	return nil
}

func (c *Memory[K, V]) expired(e *entry[K, V], now time.Time) bool {
	return !e.expiresAt.IsZero() && !now.Before(e.expiresAt)
}
func (c *Memory[K, V]) overCapacityLocked() bool {
	return (c.cfg.MaxEntries > 0 && len(c.items) > c.cfg.MaxEntries) || (c.cfg.MaxCost > 0 && c.cost > c.cfg.MaxCost)
}
func (c *Memory[K, V]) removeLocked(e *entry[K, V], reason EvictionReason) eviction[K, V] {
	delete(c.items, e.key)
	c.lru.Remove(e.lru)
	c.cost -= e.cost
	return eviction[K, V]{e.key, e.value, reason}
}
func (c *Memory[K, V]) notify(events ...eviction[K, V]) {
	if c.cfg.OnEvict == nil {
		return
	}
	for _, e := range events {
		c.cfg.OnEvict(e.key, e.value, e.reason)
	}
}

func (c *Memory[K, V]) cleanupLoop() {
	t := time.NewTicker(c.cfg.CleanupInterval)
	defer t.Stop()
	defer close(c.done)
	for {
		select {
		case <-t.C:
			// Dispatch outside the cleanup goroutine. In particular, an eviction
			// callback may call Close without waiting for this goroutine from itself.
			events := c.cleanupExpired()
			if len(events) > 0 {
				go c.notify(events...)
			}
		case <-c.stop:
			return
		}
	}
}
func (c *Memory[K, V]) cleanupExpired() []eviction[K, V] {
	now := c.cfg.Clock.Now()
	var events []eviction[K, V]
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	for _, e := range c.items {
		if c.expired(e, now) {
			events = append(events, c.removeLocked(e, EvictedExpired))
			c.metrics.expirations.Add(1)
		}
	}
	c.mu.Unlock()
	return events
}
