// Package cache provides multi-index memory cache with Redis support.
//
// This package offers a generic, thread-safe caching solution with the following features:
//   - Multi-index lookup support (O(1) lookups by different keys)
//   - Hash-based change detection
//   - Redis cache adapter for distributed scenarios
//   - Automatic TTL management
//
// Example usage:
//
//	config := cache.DefaultConfig[User]().WithPrimaryKey(func(u User) string { return u.ID })
//	cache := cache.NewMultiIndexCache[User](config)
//	cache.AddIndex("email", func(u User) string { return u.Email })
//	cache.Set(users)
//	user, ok := cache.GetByIndex("email", "user@example.com")
package cache

// Cache provides the basic cache interface.
type Cache[K comparable, V any] interface {
	// Get retrieves a value by its primary key.
	Get(key K) (V, bool)

	// Set stores a value with its primary key.
	Set(key K, value V)

	// Delete removes a value by its primary key.
	Delete(key K)

	// GetAll returns all cached values.
	GetAll() []V

	// Len returns the number of cached items.
	Len() int

	// Clear removes all items from the cache.
	Clear()

	// GetHash returns a hash representing the current cache state.
	// Useful for detecting changes.
	GetHash() string
}

// MultiIndexCache extends Cache with multi-index lookup capabilities.
type MultiIndexCache[V any] interface {
	// GetByIndex retrieves a value by a named index.
	// Returns the value and true if found, zero value and false otherwise.
	GetByIndex(indexName string, key string) (V, bool)

	// AddIndex registers a new index with a key extraction function.
	// The keyFunc extracts the index key from a value.
	AddIndex(name string, keyFunc func(V) string)

	// RemoveIndex removes an index by name.
	RemoveIndex(name string)

	// HasIndex checks if an index exists.
	HasIndex(name string) bool

	// Set stores all values and rebuilds all indexes.
	Set(values []V)

	// GetAll returns all cached values in insertion order.
	GetAll() []V

	// Len returns the number of cached items.
	Len() int

	// Clear removes all items from the cache.
	Clear()

	// GetHash returns a hash representing the current cache state.
	GetHash() string

	// Iterate applies a function to each cached value.
	// If the function returns false, iteration stops.
	Iterate(fn func(value V) bool)
}

// HashFunc defines a function that computes a hash for a value.
type HashFunc[V any] func(values []V) string

// KeyFunc defines a function that extracts a key from a value.
type KeyFunc[V any] func(value V) string

// ValidateFunc defines a function that validates a value.
// Returns an error if the value is invalid.
type ValidateFunc[V any] func(value V) error

// NormalizeFunc defines a function that normalizes a value.
// Returns the normalized value.
type NormalizeFunc[V any] func(value V) V
