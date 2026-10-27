// Package redisadapter adapts redis-kit's per-key cache to ttlcache hybrid.Store.
package redisadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/redis/go-redis/v9"
	rediskit "github.com/soulteary/redis-kit/cache"
)

// Store adapts redis-kit/cache to hybrid.Store.
type Store[K comparable, V any] struct {
	cache  *rediskit.RedisCache
	client redis.UniversalClient
	prefix string
	key    func(K) string
}

var getWithPTTL = redis.NewScript(`
local value = redis.call('GET', KEYS[1])
if not value then
  return nil
end
return {value, redis.call('PTTL', KEYS[1])}
`)

// New creates a Store. A nil key function formats keys with fmt.Sprint.
func New[K comparable, V any](client redis.UniversalClient, prefix string, key func(K) string) *Store[K, V] {
	if key == nil {
		key = func(k K) string { return fmt.Sprint(k) }
	}
	return &Store[K, V]{cache: rediskit.NewCache(client, prefix), client: client, prefix: prefix, key: key}
}

// Get returns a value and its remaining Redis TTL.
func (s *Store[K, V]) Get(ctx context.Context, key K) (V, time.Duration, bool, error) {
	var value V
	if isNil(s.client) {
		return value, 0, false, errors.New("redis client is nil")
	}
	result, err := getWithPTTL.Run(ctx, s.client, []string{s.prefix + s.key(key)}).Result()
	if errors.Is(err, redis.Nil) {
		return value, 0, false, nil
	}
	if err != nil {
		return value, 0, false, err
	}
	parts, ok := result.([]interface{})
	if !ok || len(parts) != 2 {
		return value, 0, false, fmt.Errorf("redisadapter: unexpected atomic get response %T", result)
	}
	data, ok := parts[0].(string)
	if !ok {
		return value, 0, false, fmt.Errorf("redisadapter: unexpected value response %T", parts[0])
	}
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return value, 0, false, fmt.Errorf("redisadapter: unmarshal value: %w", err)
	}
	pttl, ok := parts[1].(int64)
	if !ok {
		return value, 0, false, fmt.Errorf("redisadapter: unexpected TTL response %T", parts[1])
	}
	if pttl == -1 {
		return value, -1, true, nil
	}
	if pttl <= 0 {
		return value, 0, false, nil
	}
	return value, time.Duration(pttl) * time.Millisecond, true, nil
}

// Set stores value with ttl.
func (s *Store[K, V]) Set(ctx context.Context, key K, value V, ttl time.Duration) error {
	return s.cache.Set(ctx, s.key(key), value, ttl)
}

// Delete removes key from Redis.
func (s *Store[K, V]) Delete(ctx context.Context, key K) error {
	return s.cache.Del(ctx, s.key(key))
}

func isNil(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	return reflect.ValueOf(client).IsNil()
}
