// Package redisadapter adapts redis-kit's per-key cache to ttlcache hybrid.Store.
package redisadapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	rediskit "github.com/soulteary/redis-kit/cache"
)

// Store adapts redis-kit/cache to hybrid.Store.
type Store[K comparable, V any] struct {
	cache *rediskit.RedisCache
	key   func(K) string
}

// New creates a Store. A nil key function formats keys with fmt.Sprint.
func New[K comparable, V any](client redis.UniversalClient, prefix string, key func(K) string) *Store[K, V] {
	if key == nil {
		key = func(k K) string { return fmt.Sprint(k) }
	}
	return &Store[K, V]{cache: rediskit.NewCache(client, prefix), key: key}
}

// Get returns a value and its remaining Redis TTL.
func (s *Store[K, V]) Get(ctx context.Context, key K) (V, time.Duration, bool, error) {
	var value V
	err := s.cache.Get(ctx, s.key(key), &value)
	if errors.Is(err, rediskit.ErrKeyNotFound) || errors.Is(err, redis.Nil) {
		return value, 0, false, nil
	}
	if err != nil {
		return value, 0, false, err
	}
	ttl, err := s.cache.TTL(ctx, s.key(key))
	if err != nil {
		return value, 0, false, err
	}
	if ttl == -1 {
		return value, -1, true, nil
	}
	if ttl <= 0 {
		return value, 0, false, nil
	}
	return value, ttl, true, nil
}

// Set stores value with ttl.
func (s *Store[K, V]) Set(ctx context.Context, key K, value V, ttl time.Duration) error {
	return s.cache.Set(ctx, s.key(key), value, ttl)
}

// Delete removes key from Redis.
func (s *Store[K, V]) Delete(ctx context.Context, key K) error {
	return s.cache.Del(ctx, s.key(key))
}
