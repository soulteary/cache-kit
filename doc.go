// Package cache provides a generic, thread-safe multi-index memory cache.
//
// The cache keeps a set of values addressable by a primary key and by any
// number of secondary indexes, all O(1), and computes a content hash so a
// caller can tell whether the set has actually changed.
//
//   - Multi-index lookup (O(1) lookups by different keys)
//   - Hash-based change detection
//   - Insertion order preserved
//   - Index collision reporting
//
// Example usage:
//
//	config := cache.DefaultConfig[User]().WithPrimaryKey(func(u User) string { return u.ID })
//	c := cache.NewMultiIndexCache[User](config)
//	c.AddIndex("email", func(u User) string { return u.Email })
//	c.Set(users)
//	user, ok := c.GetByIndex("email", "user@example.com")
//
// # Layout
//
// This package depends on nothing outside the standard library. The independent
// [github.com/soulteary/cache-kit/v2/ttlcache] package is also stdlib-only.
// Redis integrations live in
// [github.com/soulteary/cache-kit/v2/rediscache] and
// [github.com/soulteary/cache-kit/v2/ttlcache/redisadapter], so programs that
// only use the in-memory packages do not link go-redis or redis-kit.
// Importing a Redis subpackage is what pulls those dependencies in:
//
//	import "github.com/soulteary/cache-kit/v2/rediscache"
//
//	c := rediscache.New[User](client, rediscache.DefaultConfig())
//	hybrid := rediscache.NewHybrid[User](config, client, rediscache.DefaultConfig())
//
// Go's package linker preserves this split. The module's go.mod still declares
// the Redis dependencies, so they remain visible to module graph inspection
// and can participate in minimal version selection.
package cache
