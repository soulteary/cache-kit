// Package rediscache stores a set of values in Redis, with a version counter
// for change detection.
//
// It lives in its own package so that importing the root package does not drag
// go-redis -- and with it cespare/xxhash, go.uber.org/atomic and
// golang.org/x/sys -- into binaries that only ever use the memory cache. A
// service that indexes a slice in RAM pays nothing for Redis support existing;
// only importing this package links it in.
//
// The root package keeps everything that does not need a Redis client: the
// memory cache, the index machinery and the content hash. This package adds
// the Redis-backed cache and [Hybrid], which pairs the two.
//
//	c := rediscache.New[User](client, rediscache.DefaultConfig().WithKeyPrefix("users:"))
//	if err := c.Set(users); err != nil {
//		return err
//	}
package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client is the part of a go-redis client this package uses. *redis.Client,
// *redis.ClusterClient, *redis.Ring and redis.UniversalClient all satisfy it,
// so the same cache works against a standalone server, a cluster and a
// Sentinel failover setup.
//
// One caveat for the sharded clients: Set, SetWithTTL and Clear write the data
// key and the version key in a single transactional pipeline, and Redis
// refuses a MULTI spanning two slots. Give the pair a common hash tag --
// KeyPrefix "{users}:" rather than "users:" -- so both keys land on the same
// node.
type Client interface {
	redis.Scripter

	Get(ctx context.Context, key string) *redis.StringCmd
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
	TTL(ctx context.Context, key string) *redis.DurationCmd
	Pipeline() redis.Pipeliner
	TxPipeline() redis.Pipeliner
}

// Config holds configuration for the Redis cache.
type Config struct {
	// KeyPrefix is prepended to all Redis keys. Use a unique prefix per cache to avoid key collision.
	KeyPrefix string

	// VersionKeySuffix is appended to the key prefix for version tracking.
	// Default: ":version"
	VersionKeySuffix string

	// TTL is the default time-to-live for cached data. Must be positive; otherwise a default is used at Set time.
	// Default: 1 hour
	TTL time.Duration

	// OperationTimeout is the timeout for Redis operations.
	// Default: 5 seconds
	OperationTimeout time.Duration

	// MaxValueBytes limits the size of the value read from Redis in Get(). If <= 0, no limit is applied.
	// Default: 16MB.
	//
	// The size is checked with STRLEN before the value is fetched, so an
	// oversized value is never pulled into memory. Checking len(data) after
	// GET, as this used to, only prevented the unmarshal -- the allocation the
	// limit exists to avoid had already happened.
	MaxValueBytes int
}

// Default max value size for Get (16 MiB).
const defaultMaxValueBytes = 16 * 1024 * 1024

// DefaultConfig returns a default Redis configuration.
func DefaultConfig() *Config {
	return &Config{
		KeyPrefix:        "cache:",
		VersionKeySuffix: ":version",
		TTL:              1 * time.Hour,
		OperationTimeout: 5 * time.Second,
		MaxValueBytes:    defaultMaxValueBytes,
	}
}

// WithKeyPrefix sets the key prefix.
func (c *Config) WithKeyPrefix(prefix string) *Config {
	c.KeyPrefix = prefix
	return c
}

// WithVersionKeySuffix sets the version key suffix.
func (c *Config) WithVersionKeySuffix(suffix string) *Config {
	c.VersionKeySuffix = suffix
	return c
}

// WithTTL sets the TTL.
func (c *Config) WithTTL(ttl time.Duration) *Config {
	c.TTL = ttl
	return c
}

// WithOperationTimeout sets the operation timeout.
func (c *Config) WithOperationTimeout(timeout time.Duration) *Config {
	c.OperationTimeout = timeout
	return c
}

// WithMaxValueBytes sets the maximum allowed size in bytes for a value read from Redis in Get().
// Values larger than this are rejected to prevent OOM. Use 0 or negative to disable the limit.
func (c *Config) WithMaxValueBytes(n int) *Config {
	c.MaxValueBytes = n
	return c
}

// maxKeyLen is the maximum allowed length for a Redis key (data or version).
// Prevents key space abuse and ensures predictable behavior.
const maxKeyLen = 512

// errNilClient is returned by every method when there is no client to talk to.
var errNilClient = errors.New("redis client is nil")

// validateKeys panics if dataKey or versionKey are invalid: empty, identical, or too long.
// Use a unique KeyPrefix (or key for NewWithKey) per cache to avoid key collision.
func validateKeys(dataKey, versionKey string) {
	if dataKey == "" {
		panic("cache-kit: Redis data key must not be empty; use a non-empty KeyPrefix or key")
	}
	if versionKey == "" || versionKey == dataKey {
		panic("cache-kit: Redis version key must not be empty and must differ from data key; set VersionKeySuffix")
	}
	if len(dataKey) > maxKeyLen || len(versionKey) > maxKeyLen {
		panic("cache-kit: Redis key length must not exceed 512 bytes")
	}
}

// Cache provides a Redis-based cache implementation.
// It supports versioning for cache invalidation detection.
type Cache[V any] struct {
	client    Client
	config    *Config
	key       string // main data key
	hasClient bool   // see isNil: guards against a typed nil in client
}

// New creates a new Redis cache with the given client and configuration.
// KeyPrefix and VersionKeySuffix must be non-empty; use a unique prefix per cache to avoid key collision.
func New[V any](client Client, config *Config) *Cache[V] {
	if config == nil {
		config = DefaultConfig()
	}
	if config.KeyPrefix == "" {
		panic("cache-kit: Redis KeyPrefix must not be empty; use a unique prefix per cache")
	}
	if config.VersionKeySuffix == "" {
		panic("cache-kit: Redis VersionKeySuffix must not be empty")
	}
	dataKey := config.KeyPrefix + "data"
	versionKey := dataKey + config.VersionKeySuffix
	validateKeys(dataKey, versionKey)
	return &Cache[V]{
		client:    client,
		config:    config,
		key:       dataKey,
		hasClient: !isNil(client),
	}
}

// NewWithKey creates a new Redis cache with a custom key name.
// The key must be non-empty; VersionKeySuffix must be non-empty. Use a unique key per cache to avoid key collision.
func NewWithKey[V any](client Client, key string, config *Config) *Cache[V] {
	if config == nil {
		config = DefaultConfig()
	}
	if key == "" {
		panic("cache-kit: Redis key must not be empty; use a unique key per cache")
	}
	if config.VersionKeySuffix == "" {
		panic("cache-kit: Redis VersionKeySuffix must not be empty")
	}
	versionKey := key + config.VersionKeySuffix
	validateKeys(key, versionKey)
	return &Cache[V]{
		client:    client,
		config:    config,
		key:       key,
		hasClient: !isNil(client),
	}
}

// isNil reports whether there is no client to talk to. Client is an interface,
// so a plain client == nil misses the case that actually reaches here -- a nil
// *redis.Client stored in it, which a caller gets from an unassigned field or
// a constructor that returned early. Calling a command on that panics, where
// the caller has every reason to expect the "redis client is nil" error the
// concrete-typed version returned.
func isNil(c Client) bool {
	if c == nil {
		return true
	}
	v := reflect.ValueOf(c)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// getContext creates a context with timeout.
//
// NOTE: this is rooted at context.Background, so a caller's cancellation and
// trace context do not reach Redis. Every method here takes no context
// parameter, so fixing that means changing the exported signatures; it is
// called out rather than changed.
func (c *Cache[V]) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.config.OperationTimeout)
}

// versionKey returns the version key for this cache.
func (c *Cache[V]) versionKey() string {
	return c.key + c.config.VersionKeySuffix
}

// effectiveTTL returns the TTL to use; if the given ttl is <= 0, uses config TTL, or 1 hour as fallback.
func (c *Cache[V]) effectiveTTL(ttl time.Duration) time.Duration {
	if ttl > 0 {
		return ttl
	}
	if c.config.TTL > 0 {
		return c.config.TTL
	}
	return 1 * time.Hour
}

// Set stores values in Redis and increments the version.
func (c *Cache[V]) Set(values []V) error {
	if !c.hasClient {
		return errNilClient
	}

	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("failed to marshal values: %w", err)
	}

	ctx, cancel := c.getContext()
	defer cancel()

	ttl := c.effectiveTTL(c.config.TTL)
	// TxPipeline (MULTI/EXEC), not Pipeline: a plain pipeline is only batching,
	// so two concurrent writers could interleave and leave the data from one
	// paired with the version from the other -- which defeats the point of
	// having a version at all.
	//
	// The version key is deliberately NOT given a TTL. Expiring it reset the
	// counter to zero, so a consumer comparing "is the version higher than what
	// I last saw" would stop refreshing after the reset. It is a single
	// integer; Clear removes it explicitly.
	pipe := c.client.TxPipeline()
	pipe.Set(ctx, c.key, data, ttl)
	pipe.Incr(ctx, c.versionKey())
	pipe.Persist(ctx, c.versionKey())

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set cache: %w", err)
	}

	return nil
}

// Get retrieves values from Redis.
// Returns an empty slice if the key doesn't exist.
// If the stored value exceeds MaxValueBytes (when set in config), returns an error to prevent OOM.
func (c *Cache[V]) Get() ([]V, error) {
	if !c.hasClient {
		return nil, errNilClient
	}

	ctx, cancel := c.getContext()
	defer cancel()

	// Measure and fetch ATOMICALLY. Reading the value and then measuring it
	// does not prevent the allocation the limit exists to prevent -- by the
	// time len(data) can be compared, the oversized value is already in
	// memory. Measuring first with a separate STRLEN has the opposite
	// problem: another writer can replace the key between the two commands,
	// so the value that arrives can exceed the limit anyway. The script does
	// both in one round trip, under Redis's single-threaded execution.
	data, err := c.getBounded(ctx)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return []V{}, nil
	}

	var values []V
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("failed to unmarshal values: %w", err)
	}

	return values, nil
}

// Exists checks if the cache key exists.
func (c *Cache[V]) Exists() (bool, error) {
	if !c.hasClient {
		return false, errNilClient
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
func (c *Cache[V]) GetVersion() (int64, error) {
	if !c.hasClient {
		return 0, errNilClient
	}

	ctx, cancel := c.getContext()
	defer cancel()

	version, err := c.client.Get(ctx, c.versionKey()).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get version: %w", err)
	}

	return version, nil
}

// Clear deletes the cache key and the version key.
// After Clear(), GetVersion() returns 0 (version key is removed).
func (c *Cache[V]) Clear() error {
	if !c.hasClient {
		return errNilClient
	}

	ctx, cancel := c.getContext()
	defer cancel()

	pipe := c.client.TxPipeline()
	pipe.Del(ctx, c.key)
	pipe.Del(ctx, c.versionKey())
	_, err := pipe.Exec(ctx)
	return err
}

// SetWithTTL stores values with a custom TTL.
func (c *Cache[V]) SetWithTTL(values []V, ttl time.Duration) error {
	if !c.hasClient {
		return errNilClient
	}

	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("failed to marshal values: %w", err)
	}

	ctx, cancel := c.getContext()
	defer cancel()

	effectiveTTL := c.effectiveTTL(ttl)
	// See Set: atomic, and the version key outlives the data.
	pipe := c.client.TxPipeline()
	pipe.Set(ctx, c.key, data, effectiveTTL)
	pipe.Incr(ctx, c.versionKey())
	pipe.Persist(ctx, c.versionKey())

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set cache: %w", err)
	}

	return nil
}

// TTL returns the remaining TTL for the cache key.
func (c *Cache[V]) TTL() (time.Duration, error) {
	if !c.hasClient {
		return 0, errNilClient
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
func (c *Cache[V]) Refresh() error {
	if !c.hasClient {
		return errNilClient
	}

	ctx, cancel := c.getContext()
	defer cancel()

	ttl := c.effectiveTTL(c.config.TTL)
	pipe := c.client.Pipeline()
	pipe.Expire(ctx, c.key, ttl)
	// The version key stays persistent, as Set leaves it. Expiring it here
	// meant any cache that gets refreshed lost its version once the TTL
	// elapsed; the next Set then restarted the counter at 1, and a consumer
	// that had already observed a higher version stopped seeing updates --
	// exactly the reset Set was changed to prevent. Persist also repairs a
	// version key written by an earlier version of this package.
	pipe.Persist(ctx, c.versionKey())

	_, err := pipe.Exec(ctx)
	return err
}

// boundedGetScript measures a key and returns its value in one atomic step.
//
// ARGV[1] is the byte limit; 0 means unlimited. The reply is a two-element
// array: {"ok", value}, {"missing", ""} or {"toolarge", size}.
var boundedGetScript = redis.NewScript(`
local limit = tonumber(ARGV[1])
if limit > 0 then
  local size = redis.call('STRLEN', KEYS[1])
  if size > limit then
    return {'toolarge', tostring(size)}
  end
end
local value = redis.call('GET', KEYS[1])
if value == false then
  return {'missing', ''}
end
return {'ok', value}
`)

// getBounded fetches the cache value, refusing one over MaxValueBytes.
//
// It returns (nil, nil) when the key does not exist.
func (c *Cache[V]) getBounded(ctx context.Context) ([]byte, error) {
	maxBytes := c.config.MaxValueBytes
	if maxBytes <= 0 {
		data, err := c.client.Get(ctx, c.key).Bytes()
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get cache: %w", err)
		}
		return data, nil
	}

	reply, err := boundedGetScript.Run(ctx, c.client, []string{c.key}, maxBytes).Slice()
	if err != nil {
		return nil, fmt.Errorf("failed to get cache: %w", err)
	}
	if len(reply) != 2 {
		return nil, fmt.Errorf("failed to get cache: unexpected reply of %d elements", len(reply))
	}

	status, _ := reply[0].(string)
	payload, _ := reply[1].(string)

	switch status {
	case "missing":
		return nil, nil
	case "toolarge":
		return nil, fmt.Errorf("cache value size %s exceeds max allowed %d", payload, maxBytes)
	case "ok":
		return []byte(payload), nil
	default:
		return nil, fmt.Errorf("failed to get cache: unexpected status %q", status)
	}
}
