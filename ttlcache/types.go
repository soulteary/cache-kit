// Package ttlcache provides a generic, thread-safe, per-key TTL cache.
// It is independent from cache-kit's collection-oriented multi-index cache.
package ttlcache

import (
	"errors"
	"time"
)

// NoExpiration stores an entry without an expiry time.
const NoExpiration time.Duration = -1

var (
	// ErrClosed is returned when a write is attempted after Close.
	ErrClosed = errors.New("ttlcache: cache is closed")
	// ErrInvalidTTL is returned for a negative TTL other than NoExpiration.
	ErrInvalidTTL = errors.New("ttlcache: ttl must be positive, zero, or NoExpiration")
	// ErrInvalidCost is returned when Config.Cost reports a negative value.
	ErrInvalidCost = errors.New("ttlcache: item cost must not be negative")
	// ErrItemTooLarge is returned when one entry exceeds MaxItemCost or MaxCost.
	ErrItemTooLarge = errors.New("ttlcache: item exceeds configured capacity")
)

// EvictionReason explains why an entry was passed to Config.OnEvict.
type EvictionReason uint8

const (
	// EvictedExpired means the entry reached its TTL.
	EvictedExpired EvictionReason = iota + 1
	// EvictedCapacity means an LRU entry was removed to satisfy a limit.
	EvictedCapacity
	// EvictedDeleted means Delete removed the entry.
	EvictedDeleted
	// EvictedReplaced means Set replaced the value for an existing key.
	EvictedReplaced
	// EvictedCleared means Clear removed the entry.
	EvictedCleared
)

// Stats is a point-in-time snapshot. The counters are cumulative for the life
// of the cache; Entries and Cost describe its current contents.
type Stats struct {
	Hits, Misses, Sets, Deletes, Expirations, Evictions, Rejections uint64
	Entries                                                         int
	Cost                                                            int64
}

// Config controls expiry, capacity and eviction notifications. Zero capacity
// limits are unlimited. A nil Cost function assigns a cost of one per entry.
type Config[K comparable, V any] struct {
	DefaultTTL      time.Duration
	CleanupInterval time.Duration
	MaxEntries      int
	MaxCost         int64
	MaxItemCost     int64
	Cost            func(K, V) int64
	Clock           Clock
	OnEvict         func(K, V, EvictionReason)
}

// DefaultConfig returns a five-minute TTL and a one-minute cleanup interval.
func DefaultConfig[K comparable, V any]() Config[K, V] {
	return Config[K, V]{DefaultTTL: 5 * time.Minute, CleanupInterval: time.Minute}
}

// Cache is the common interface implemented by Memory.
type Cache[K comparable, V any] interface {
	Get(K) (V, bool)
	Set(K, V, time.Duration) error
	Delete(K) bool
	Expire(K, time.Duration) bool
	TTL(K) (time.Duration, bool)
	Len() int
	Clear()
	Stats() Stats
	Close() error
}
