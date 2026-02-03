package cache

import (
	"strings"
	"sync"
)

// MemoryCache provides a thread-safe, multi-index memory cache.
//
// It supports O(1) lookups by primary key and by any registered index.
// The cache maintains insertion order and provides hash-based change detection.
//
//nolint:govet // field order optimized for alignment
type MemoryCache[V any] struct {
	mu       sync.RWMutex
	config   *Config[V]
	data     map[string]V                 // primary key -> value
	order    []string                     // insertion order (primary keys)
	indexes  map[string]map[string]string // index name -> index key -> primary key
	indexFns map[string]KeyFunc[V]        // index name -> key extraction function
	hash     string                       // cached hash value
}

// NewMultiIndexCache creates a new multi-index memory cache.
// The config must have a PrimaryKeyFunc set.
func NewMultiIndexCache[V any](config *Config[V]) *MemoryCache[V] {
	if config == nil {
		config = DefaultConfig[V]()
	}
	return &MemoryCache[V]{
		config:   config,
		data:     make(map[string]V),
		order:    make([]string, 0),
		indexes:  make(map[string]map[string]string),
		indexFns: make(map[string]KeyFunc[V]),
	}
}

// AddIndex registers a new index with a key extraction function.
// The keyFunc extracts the index key from a value.
// If an index with the same name exists, it will be replaced.
func (c *MemoryCache[V]) AddIndex(name string, keyFunc KeyFunc[V]) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.indexFns[name] = keyFunc
	c.indexes[name] = make(map[string]string)

	// Rebuild index for existing data
	for pk, v := range c.data {
		indexKey := keyFunc(v)
		if indexKey != "" {
			c.indexes[name][c.normalizeKey(indexKey)] = pk
		}
	}
}

// RemoveIndex removes an index by name.
func (c *MemoryCache[V]) RemoveIndex(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.indexFns, name)
	delete(c.indexes, name)
}

// HasIndex checks if an index exists.
func (c *MemoryCache[V]) HasIndex(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	_, exists := c.indexFns[name]
	return exists
}

// GetByIndex retrieves a value by a named index.
// Returns the value and true if found, zero value and false otherwise.
func (c *MemoryCache[V]) GetByIndex(indexName string, key string) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var zero V
	index, exists := c.indexes[indexName]
	if !exists {
		return zero, false
	}

	pk, exists := index[c.normalizeKey(key)]
	if !exists {
		return zero, false
	}

	value, exists := c.data[pk]
	return value, exists
}

// Get retrieves a value by its primary key.
func (c *MemoryCache[V]) Get(key string) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	value, exists := c.data[key]
	return value, exists
}

// Set stores all values and rebuilds all indexes.
// Values are validated and normalized if the corresponding functions are set.
// Duplicate primary keys will be updated (last one wins).
// Panics if PrimaryKeyFunc is nil and len(values) > 0; set PrimaryKeyFunc via config before use with non-empty data.
func (c *MemoryCache[V]) Set(values []V) {
	if len(values) > 0 && c.config.PrimaryKeyFunc == nil {
		panic("cache-kit: MultiIndexCache requires PrimaryKeyFunc when setting non-empty data; set it via config.WithPrimaryKey()")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Clear existing data
	c.data = make(map[string]V, len(values))
	c.order = make([]string, 0, len(values))

	// Clear all indexes
	for name := range c.indexes {
		c.indexes[name] = make(map[string]string, len(values))
	}

	// Process each value
	for _, v := range values {
		// Normalize if function is set
		if c.config.NormalizeFunc != nil {
			v = c.config.NormalizeFunc(v)
		}

		// Validate if function is set
		if c.config.ValidateFunc != nil {
			if err := c.config.ValidateFunc(v); err != nil {
				continue // Skip invalid values
			}
		}

		// Get primary key
		var pk string
		if c.config.PrimaryKeyFunc != nil {
			pk = c.config.PrimaryKeyFunc(v)
		}
		if pk == "" {
			continue // Skip values without primary key
		}

		// Track insertion order
		if _, exists := c.data[pk]; !exists {
			c.order = append(c.order, pk)
		}

		// Store value
		c.data[pk] = v

		// Update all indexes
		for name, keyFunc := range c.indexFns {
			indexKey := keyFunc(v)
			if indexKey != "" {
				c.indexes[name][c.normalizeKey(indexKey)] = pk
			}
		}
	}

	// Calculate and cache hash
	c.hash = c.calculateHash()
}

// GetAll returns all cached values in insertion order.
func (c *MemoryCache[V]) GetAll() []V {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]V, 0, len(c.order))
	for _, pk := range c.order {
		if v, exists := c.data[pk]; exists {
			result = append(result, v)
		}
	}
	return result
}

// Len returns the number of cached items.
func (c *MemoryCache[V]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.data)
}

// Clear removes all items from the cache.
func (c *MemoryCache[V]) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data = make(map[string]V)
	c.order = make([]string, 0)
	for name := range c.indexes {
		c.indexes[name] = make(map[string]string)
	}
	c.hash = ""
}

// GetHash returns a hash representing the current cache state.
func (c *MemoryCache[V]) GetHash() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.hash
}

// Iterate applies a function to each cached value in insertion order.
// If the function returns false, iteration stops.
// The callback must not panic; if it does, the read lock may block other goroutines until recovery.
func (c *MemoryCache[V]) Iterate(fn func(value V) bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, pk := range c.order {
		if v, exists := c.data[pk]; exists {
			if !fn(v) {
				return
			}
		}
	}
}

// calculateHash computes the hash of the current cache contents.
func (c *MemoryCache[V]) calculateHash() string {
	if len(c.data) == 0 {
		return sha256Hash("empty")
	}

	// Get values in order
	values := make([]V, 0, len(c.order))
	for _, pk := range c.order {
		if v, exists := c.data[pk]; exists {
			values = append(values, v)
		}
	}

	// Sort if sort function is provided
	if c.config.SortFunc != nil {
		values = c.config.SortFunc(values)
	}

	// Use custom hash function if provided
	if c.config.HashFunc != nil {
		return c.config.HashFunc(values)
	}

	return defaultHashFunc(values)
}

// normalizeKey normalizes an index key (lowercase, trimmed).
func (c *MemoryCache[V]) normalizeKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// IndexCount returns the number of registered indexes.
func (c *MemoryCache[V]) IndexCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.indexFns)
}

// IndexNames returns the names of all registered indexes.
func (c *MemoryCache[V]) IndexNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := make([]string, 0, len(c.indexFns))
	for name := range c.indexFns {
		names = append(names, name)
	}
	return names
}
