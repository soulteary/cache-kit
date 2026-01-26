package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Config holds configuration for the cache.
type Config[V any] struct {
	// PrimaryKeyFunc extracts the primary key from a value.
	// This is required for multi-index cache.
	PrimaryKeyFunc KeyFunc[V]

	// HashFunc computes a hash for the cache contents.
	// If nil, a default hash function is used.
	HashFunc HashFunc[V]

	// ValidateFunc validates a value before storing.
	// If nil, all values are accepted.
	ValidateFunc ValidateFunc[V]

	// NormalizeFunc normalizes a value before storing.
	// If nil, values are stored as-is.
	NormalizeFunc NormalizeFunc[V]

	// SortFunc is used for deterministic hash calculation.
	// If nil, values are hashed in insertion order.
	SortFunc func(values []V) []V
}

// DefaultConfig returns a default configuration.
// Note: PrimaryKeyFunc must be set before use with MultiIndexCache.
func DefaultConfig[V any]() *Config[V] {
	return &Config[V]{
		HashFunc: defaultHashFunc[V],
	}
}

// WithPrimaryKey sets the primary key extraction function.
func (c *Config[V]) WithPrimaryKey(fn KeyFunc[V]) *Config[V] {
	c.PrimaryKeyFunc = fn
	return c
}

// WithHashFunc sets a custom hash function.
func (c *Config[V]) WithHashFunc(fn HashFunc[V]) *Config[V] {
	c.HashFunc = fn
	return c
}

// WithValidateFunc sets a validation function.
func (c *Config[V]) WithValidateFunc(fn ValidateFunc[V]) *Config[V] {
	c.ValidateFunc = fn
	return c
}

// WithNormalizeFunc sets a normalization function.
func (c *Config[V]) WithNormalizeFunc(fn NormalizeFunc[V]) *Config[V] {
	c.NormalizeFunc = fn
	return c
}

// WithSortFunc sets a sort function for deterministic hashing.
func (c *Config[V]) WithSortFunc(fn func(values []V) []V) *Config[V] {
	c.SortFunc = fn
	return c
}

// defaultHashFunc provides a simple hash implementation.
func defaultHashFunc[V any](values []V) string {
	if len(values) == 0 {
		return sha256Hash("empty")
	}

	var sb strings.Builder
	for _, v := range values {
		sb.WriteString(fmt.Sprintf("%v\n", v))
	}
	return sha256Hash(sb.String())
}

// sha256Hash computes SHA256 hash of a string.
func sha256Hash(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// RedisConfig holds configuration for Redis cache.
type RedisConfig struct {
	// KeyPrefix is prepended to all Redis keys.
	KeyPrefix string

	// VersionKeySuffix is appended to the key prefix for version tracking.
	// Default: ":version"
	VersionKeySuffix string

	// TTL is the default time-to-live for cached data.
	// Default: 1 hour
	TTL time.Duration

	// OperationTimeout is the timeout for Redis operations.
	// Default: 5 seconds
	OperationTimeout time.Duration
}

// DefaultRedisConfig returns a default Redis configuration.
func DefaultRedisConfig() *RedisConfig {
	return &RedisConfig{
		KeyPrefix:        "cache:",
		VersionKeySuffix: ":version",
		TTL:              1 * time.Hour,
		OperationTimeout: 5 * time.Second,
	}
}

// WithKeyPrefix sets the key prefix.
func (c *RedisConfig) WithKeyPrefix(prefix string) *RedisConfig {
	c.KeyPrefix = prefix
	return c
}

// WithVersionKeySuffix sets the version key suffix.
func (c *RedisConfig) WithVersionKeySuffix(suffix string) *RedisConfig {
	c.VersionKeySuffix = suffix
	return c
}

// WithTTL sets the TTL.
func (c *RedisConfig) WithTTL(ttl time.Duration) *RedisConfig {
	c.TTL = ttl
	return c
}

// WithOperationTimeout sets the operation timeout.
func (c *RedisConfig) WithOperationTimeout(timeout time.Duration) *RedisConfig {
	c.OperationTimeout = timeout
	return c
}

// StringSorter provides a helper for sorting slices by a string key.
func StringSorter[V any](keyFunc func(V) string) func([]V) []V {
	return func(values []V) []V {
		result := make([]V, len(values))
		copy(result, values)
		sort.Slice(result, func(i, j int) bool {
			return keyFunc(result[i]) < keyFunc(result[j])
		})
		return result
	}
}
