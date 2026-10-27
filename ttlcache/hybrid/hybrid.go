// Package hybrid provides an explicit cache-aside L1/L2 composition.
package hybrid

import (
	"context"
	"fmt"
	"time"

	"github.com/soulteary/cache-kit/v2/ttlcache"
)

// Store is the L2 contract used by Cache. Get returns the remaining TTL along
// with the value so L1 never extends an L2 entry's lifetime.
type Store[K comparable, V any] interface {
	Get(context.Context, K) (V, time.Duration, bool, error)
	Set(context.Context, K, V, time.Duration) error
	Delete(context.Context, K) error
}

// WriteOrder selects which layer receives a Set first.
type WriteOrder uint8

const (
	// RedisFirst writes L2 before L1 and deletes L2 if the L1 write fails.
	RedisFirst WriteOrder = iota + 1
	// MemoryFirst writes L1 before L2 and does not roll L1 back on L2 failure.
	MemoryFirst
)

// PartialError reports an operation that changed one layer but not the other.
type PartialError struct {
	Operation string
	Primary   error
	Rollback  error
}

func (e *PartialError) Error() string {
	if e.Rollback != nil {
		return fmt.Sprintf("hybrid %s partially succeeded: %v; rollback: %v", e.Operation, e.Primary, e.Rollback)
	}
	return fmt.Sprintf("hybrid %s partially succeeded: %v", e.Operation, e.Primary)
}
func (e *PartialError) Unwrap() error { return e.Primary }

// Cache composes a Memory L1 with a caller-supplied L2 Store.
type Cache[K comparable, V any] struct {
	L1    *ttlcache.Memory[K, V]
	L2    Store[K, V]
	Order WriteOrder
}

// New creates a cache-aside L1/L2 cache. A zero order means RedisFirst.
func New[K comparable, V any](l1 *ttlcache.Memory[K, V], l2 Store[K, V], order WriteOrder) *Cache[K, V] {
	if order == 0 {
		order = RedisFirst
	}
	return &Cache[K, V]{l1, l2, order}
}

// Get checks L1, then L2, and refills L1 with L2's remaining TTL.
func (c *Cache[K, V]) Get(ctx context.Context, key K) (V, bool, error) {
	if v, ok := c.L1.Get(key); ok {
		return v, true, nil
	}
	v, ttl, ok, err := c.L2.Get(ctx, key)
	if err != nil || !ok {
		return v, ok, err
	}
	if ttl == 0 {
		return v, false, nil
	}
	if err := c.L1.Set(key, v, ttl); err != nil {
		return v, true, &PartialError{Operation: "refill", Primary: err}
	}
	return v, true, nil
}

// Set writes both layers in the configured order.
func (c *Cache[K, V]) Set(ctx context.Context, key K, value V, ttl time.Duration) error {
	if c.Order == MemoryFirst {
		if err := c.L1.Set(key, value, ttl); err != nil {
			return err
		}
		if err := c.L2.Set(ctx, key, value, ttl); err != nil {
			return &PartialError{Operation: "set", Primary: err}
		}
		return nil
	}
	if err := c.L2.Set(ctx, key, value, ttl); err != nil {
		return err
	}
	if err := c.L1.Set(key, value, ttl); err != nil {
		rollback := c.L2.Delete(ctx, key)
		return &PartialError{Operation: "set", Primary: err, Rollback: rollback}
	}
	return nil
}

// Delete removes key from both layers.
func (c *Cache[K, V]) Delete(ctx context.Context, key K) error {
	l1 := c.L1.Delete(key)
	err := c.L2.Delete(ctx, key)
	if err != nil && l1 {
		return &PartialError{Operation: "delete", Primary: err}
	}
	return err
}

// Close closes L1. The caller remains responsible for the L2 client.
func (c *Cache[K, V]) Close() error { return c.L1.Close() }
