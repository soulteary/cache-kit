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
// This package depends on nothing outside the standard library. Redis support
// lives in the [github.com/soulteary/cache-kit/v2/rediscache] subpackage, so a
// program that only caches in memory does not link go-redis -- nor
// cespare/xxhash, go.uber.org/atomic and golang.org/x/sys, which come with it.
// Importing rediscache is what pulls them in:
//
//	import "github.com/soulteary/cache-kit/v2/rediscache"
//
//	c := rediscache.New[User](client, rediscache.DefaultConfig())
//	hybrid := rediscache.NewHybrid[User](config, client, rediscache.DefaultConfig())
//
// Module graph pruning keeps that split honest: a requirement no imported
// package needs stays out of the consumer's go.mod and go.sum entirely.
package cache
