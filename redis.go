package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// maxRedisKeyLen is the maximum allowed length for a Redis key (data or version).
// Prevents key space abuse and ensures predictable behavior.
const maxRedisKeyLen = 512

// validateRedisKeys panics if dataKey or versionKey are invalid: empty, identical, or too long.
// Use a unique KeyPrefix (or key for NewRedisCacheWithKey) per cache to avoid key collision.
func validateRedisKeys(dataKey, versionKey string) {
	if dataKey == "" {
		panic("cache-kit: Redis data key must not be empty; use a non-empty KeyPrefix or key")
	}
	if versionKey == "" || versionKey == dataKey {
		panic("cache-kit: Redis version key must not be empty and must differ from data key; set VersionKeySuffix")
	}
	if len(dataKey) > maxRedisKeyLen || len(versionKey) > maxRedisKeyLen {
		panic("cache-kit: Redis key length must not exceed 512 bytes")
	}
}

// RedisCache provides a Redis-based cache implementation.
// It supports versioning for cache invalidation detection.
type RedisCache[V any] struct {
	client *redis.Client
	config *RedisConfig
	key    string // main data key
}

// NewRedisCache creates a new Redis cache with the given client and configuration.
// KeyPrefix and VersionKeySuffix must be non-empty; use a unique prefix per cache to avoid key collision.
func NewRedisCache[V any](client *redis.Client, config *RedisConfig) *RedisCache[V] {
	if config == nil {
		config = DefaultRedisConfig()
	}
	if config.KeyPrefix == "" {
		panic("cache-kit: Redis KeyPrefix must not be empty; use a unique prefix per cache")
	}
	if config.VersionKeySuffix == "" {
		panic("cache-kit: Redis VersionKeySuffix must not be empty")
	}
	dataKey := config.KeyPrefix + "data"
	versionKey := dataKey + config.VersionKeySuffix
	validateRedisKeys(dataKey, versionKey)
	return &RedisCache[V]{
		client: client,
		config: config,
		key:    dataKey,
	}
}

// NewRedisCacheWithKey creates a new Redis cache with a custom key name.
// The key must be non-empty; VersionKeySuffix must be non-empty. Use a unique key per cache to avoid key collision.
func NewRedisCacheWithKey[V any](client *redis.Client, key string, config *RedisConfig) *RedisCache[V] {
	if config == nil {
		config = DefaultRedisConfig()
	}
	if key == "" {
		panic("cache-kit: Redis key must not be empty; use a unique key per cache")
	}
	if config.VersionKeySuffix == "" {
		panic("cache-kit: Redis VersionKeySuffix must not be empty")
	}
	versionKey := key + config.VersionKeySuffix
	validateRedisKeys(key, versionKey)
	return &RedisCache[V]{
		client: client,
		config: config,
		key:    key,
	}
}

// getContext creates a context with timeout.
func (c *RedisCache[V]) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.config.OperationTimeout)
}

// versionKey returns the version key for this cache.
func (c *RedisCache[V]) versionKey() string {
	return c.key + c.config.VersionKeySuffix
}

// effectiveTTL returns the TTL to use; if the given ttl is <= 0, uses config TTL, or 1 hour as fallback.
func (c *RedisCache[V]) effectiveTTL(ttl time.Duration) time.Duration {
	if ttl > 0 {
		return ttl
	}
	if c.config.TTL > 0 {
		return c.config.TTL
	}
	return 1 * time.Hour
}

// Set stores values in Redis and increments the version.
func (c *RedisCache[V]) Set(values []V) error {
	if c.client == nil {
		return fmt.Errorf("redis client is nil")
	}

	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("failed to marshal values: %w", err)
	}

	ctx, cancel := c.getContext()
	defer cancel()

	ttl := c.effectiveTTL(c.config.TTL)
	pipe := c.client.Pipeline()
	pipe.Set(ctx, c.key, data, ttl)
	pipe.Incr(ctx, c.versionKey())
	pipe.Expire(ctx, c.versionKey(), ttl)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set cache: %w", err)
	}

	return nil
}

// Get retrieves values from Redis.
// Returns an empty slice if the key doesn't exist.
// If the stored value exceeds MaxValueBytes (when set in config), returns an error to prevent OOM.
func (c *RedisCache[V]) Get() ([]V, error) {
	if c.client == nil {
		return nil, fmt.Errorf("redis client is nil")
	}

	ctx, cancel := c.getContext()
	defer cancel()

	data, err := c.client.Get(ctx, c.key).Bytes()
	if err == redis.Nil {
		return []V{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get cache: %w", err)
	}

	maxBytes := c.config.MaxValueBytes
	if maxBytes > 0 && len(data) > maxBytes {
		return nil, fmt.Errorf("cache value size %d exceeds max allowed %d", len(data), maxBytes)
	}

	var values []V
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("failed to unmarshal values: %w", err)
	}

	return values, nil
}

// Exists checks if the cache key exists.
func (c *RedisCache[V]) Exists() (bool, error) {
	if c.client == nil {
		return false, fmt.Errorf("redis client is nil")
	}

	ctx, cancel := c.getContext()
	defer cancel()

	count, err := c.client.Exists(ctx, c.key).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check existence: %w", err)
	}

	return count > 0, nil
}

// GetVersion returns the current cache version.
// Returns 0 if the version key doesn't exist.
func (c *RedisCache[V]) GetVersion() (int64, error) {
	if c.client == nil {
		return 0, fmt.Errorf("redis client is nil")
	}

	ctx, cancel := c.getContext()
	defer cancel()

	version, err := c.client.Get(ctx, c.versionKey()).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get version: %w", err)
	}

	return version, nil
}

// Clear deletes the cache key and the version key.
// After Clear(), GetVersion() returns 0 (version key is removed).
func (c *RedisCache[V]) Clear() error {
	if c.client == nil {
		return fmt.Errorf("redis client is nil")
	}

	ctx, cancel := c.getContext()
	defer cancel()

	pipe := c.client.Pipeline()
	pipe.Del(ctx, c.key)
	pipe.Del(ctx, c.versionKey())
	_, err := pipe.Exec(ctx)
	return err
}

// SetWithTTL stores values with a custom TTL.
func (c *RedisCache[V]) SetWithTTL(values []V, ttl time.Duration) error {
	if c.client == nil {
		return fmt.Errorf("redis client is nil")
	}

	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("failed to marshal values: %w", err)
	}

	ctx, cancel := c.getContext()
	defer cancel()

	effectiveTTL := c.effectiveTTL(ttl)
	pipe := c.client.Pipeline()
	pipe.Set(ctx, c.key, data, effectiveTTL)
	pipe.Incr(ctx, c.versionKey())
	pipe.Expire(ctx, c.versionKey(), effectiveTTL)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set cache: %w", err)
	}

	return nil
}

// TTL returns the remaining TTL for the cache key.
func (c *RedisCache[V]) TTL() (time.Duration, error) {
	if c.client == nil {
		return 0, fmt.Errorf("redis client is nil")
	}

	ctx, cancel := c.getContext()
	defer cancel()

	ttl, err := c.client.TTL(ctx, c.key).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to get TTL: %w", err)
	}

	return ttl, nil
}

// Refresh extends the TTL of the cache without changing the data.
func (c *RedisCache[V]) Refresh() error {
	if c.client == nil {
		return fmt.Errorf("redis client is nil")
	}

	ctx, cancel := c.getContext()
	defer cancel()

	ttl := c.effectiveTTL(c.config.TTL)
	pipe := c.client.Pipeline()
	pipe.Expire(ctx, c.key, ttl)
	pipe.Expire(ctx, c.versionKey(), ttl)

	_, err := pipe.Exec(ctx)
	return err
}

// HybridCache combines memory cache with Redis for distributed scenarios.
// It uses memory cache for fast local access and Redis for persistence/sharing.
type HybridCache[V any] struct {
	memory *MemoryCache[V]
	redis  *RedisCache[V]
}

// NewHybridCache creates a new hybrid cache.
func NewHybridCache[V any](memoryConfig *Config[V], redisClient *redis.Client, redisConfig *RedisConfig) *HybridCache[V] {
	return &HybridCache[V]{
		memory: NewMultiIndexCache(memoryConfig),
		redis:  NewRedisCache[V](redisClient, redisConfig),
	}
}

// AddIndex registers a new index on the memory cache.
func (c *HybridCache[V]) AddIndex(name string, keyFunc KeyFunc[V]) {
	c.memory.AddIndex(name, keyFunc)
}

// Set stores values in both memory and Redis.
// Memory is updated first, then Redis. If Redis.Set fails, memory already holds the new data
// while Redis may still have the old data; the error is returned and the caller should
// retry or call LoadFromRedis to reconcile (e.g. clear memory or reload from Redis).
func (c *HybridCache[V]) Set(values []V) error {
	c.memory.Set(values)
	return c.redis.Set(values)
}

// GetByIndex retrieves a value from memory cache by index.
func (c *HybridCache[V]) GetByIndex(indexName string, key string) (V, bool) {
	return c.memory.GetByIndex(indexName, key)
}

// GetAll returns all values from memory cache.
func (c *HybridCache[V]) GetAll() []V {
	return c.memory.GetAll()
}

// LoadFromRedis loads data from Redis into memory cache.
func (c *HybridCache[V]) LoadFromRedis() error {
	values, err := c.redis.Get()
	if err != nil {
		return err
	}
	c.memory.Set(values)
	return nil
}

// SyncToRedis saves memory cache data to Redis.
func (c *HybridCache[V]) SyncToRedis() error {
	values := c.memory.GetAll()
	return c.redis.Set(values)
}

// Memory returns the underlying memory cache for direct access.
func (c *HybridCache[V]) Memory() *MemoryCache[V] {
	return c.memory
}

// Redis returns the underlying Redis cache for direct access.
func (c *HybridCache[V]) Redis() *RedisCache[V] {
	return c.redis
}
