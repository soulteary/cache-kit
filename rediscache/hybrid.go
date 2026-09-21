package rediscache

import (
	cache "github.com/soulteary/cache-kit/v2"
)

// Hybrid combines the memory cache with Redis for distributed scenarios.
// It uses the memory cache for fast local access and Redis for
// persistence/sharing.
type Hybrid[V any] struct {
	memory *cache.MemoryCache[V]
	redis  *Cache[V]
}

// NewHybrid creates a new hybrid cache.
func NewHybrid[V any](memoryConfig *cache.Config[V], redisClient Client, redisConfig *Config) *Hybrid[V] {
	return &Hybrid[V]{
		memory: cache.NewMultiIndexCache(memoryConfig),
		redis:  New[V](redisClient, redisConfig),
	}
}

// AddIndex registers a new index on the memory cache.
func (c *Hybrid[V]) AddIndex(name string, keyFunc cache.KeyFunc[V]) {
	c.memory.AddIndex(name, keyFunc)
}

// Set stores values in both memory and Redis.
// Memory is updated first, then Redis. If the Redis write fails, memory already holds the new data
// while Redis may still have the old data; the error is returned and the caller should
// retry or call LoadFromRedis to reconcile (e.g. clear memory or reload from Redis).
func (c *Hybrid[V]) Set(values []V) error {
	c.memory.Set(values)
	return c.redis.Set(values)
}

// GetByIndex retrieves a value from the memory cache by index.
func (c *Hybrid[V]) GetByIndex(indexName string, key string) (V, bool) {
	return c.memory.GetByIndex(indexName, key)
}

// GetAll returns all values from the memory cache.
func (c *Hybrid[V]) GetAll() []V {
	return c.memory.GetAll()
}

// LoadFromRedis loads data from Redis into the memory cache.
func (c *Hybrid[V]) LoadFromRedis() error {
	values, err := c.redis.Get()
	if err != nil {
		return err
	}
	c.memory.Set(values)
	return nil
}

// SyncToRedis saves memory cache data to Redis.
func (c *Hybrid[V]) SyncToRedis() error {
	values := c.memory.GetAll()
	return c.redis.Set(values)
}

// Memory returns the underlying memory cache for direct access.
func (c *Hybrid[V]) Memory() *cache.MemoryCache[V] {
	return c.memory
}

// Redis returns the underlying Redis cache for direct access.
func (c *Hybrid[V]) Redis() *Cache[V] {
	return c.redis
}
