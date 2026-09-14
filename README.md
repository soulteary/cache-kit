# cache-kit

[![Go Reference](https://pkg.go.dev/badge/github.com/soulteary/cache-kit.svg)](https://pkg.go.dev/github.com/soulteary/cache-kit)
[![Go Report Card](.github/goreportcard.svg)](.github/goreportcard-report.md)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![codecov](https://codecov.io/gh/soulteary/cache-kit/graph/badge.svg)](https://codecov.io/gh/soulteary/cache-kit)

[中文文档](README_CN.md)

A Go library for thread-safe, multi-index memory caching with Redis support.
Look values up by any number of keys in O(1), detect content changes by a
reproducible hash, and share the cache across instances through Redis.

## Features

- **Multi-index lookup**: O(1) lookups by several keys (ID, email, phone, …)
- **Thread-safe**: safe for concurrent readers and writers
- **Reproducible change detection**: a content hash that is stable across processes
- **Redis support**: a Redis adapter for distributed scenarios
- **Hybrid cache**: memory in front of Redis
- **Generic**: works with any value type through Go generics
- **Fluent configuration**: builder pattern for both configs

## Requirements

- **Go 1.27+** (`go.mod` declares `go 1.27.0`)
- `github.com/redis/go-redis/v9` for the Redis and hybrid caches
- Redis with `EVAL` support (any 2.6+) when `MaxValueBytes` is set

## Installation

```bash
go get github.com/soulteary/cache-kit
```

## Quick Start

### Memory cache with multiple indexes

```go
package main

import (
    "fmt"

    cache "github.com/soulteary/cache-kit"
)

type User struct {
    ID    string
    Email string
    Phone string
    Name  string
}

func main() {
    config := cache.DefaultConfig[User]().
        WithPrimaryKey(func(u User) string { return u.ID })

    c := cache.NewMultiIndexCache(config)

    c.AddIndex("email", func(u User) string { return u.Email })
    c.AddIndex("phone", func(u User) string { return u.Phone })

    c.Set([]User{
        {ID: "1", Email: "alice@example.com", Phone: "1111111111", Name: "Alice"},
        {ID: "2", Email: "bob@example.com", Phone: "2222222222", Name: "Bob"},
    })

    user, ok := c.Get("1")                                  // by primary key
    user, ok = c.GetByIndex("email", "bob@example.com")     // by index
    user, ok = c.GetByIndex("phone", "1111111111")
    _ = user
    _ = ok

    fmt.Println("cache hash:", c.GetHash())
}
```

### Redis cache

```go
package main

import (
    "fmt"
    "time"

    "github.com/redis/go-redis/v9"
    cache "github.com/soulteary/cache-kit"
)

func main() {
    client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
    defer client.Close()

    config := cache.DefaultRedisConfig().
        WithKeyPrefix("myapp:users:").
        WithTTL(30 * time.Minute)

    c := cache.NewRedisCache[User](client, config)

    if err := c.Set([]User{{ID: "1", Name: "Alice"}}); err != nil {
        panic(err)
    }

    users, err := c.Get()
    if err != nil {
        panic(err)
    }
    _ = users

    // A monotonically increasing counter, useful for "has anyone else written?"
    version, _ := c.GetVersion()
    fmt.Println("cache version:", version)
}
```

### Hybrid cache (memory + Redis)

```go
package main

import (
    "github.com/redis/go-redis/v9"
    cache "github.com/soulteary/cache-kit"
)

func main() {
    client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
    defer client.Close()

    memConfig := cache.DefaultConfig[User]().
        WithPrimaryKey(func(u User) string { return u.ID })
    redisConfig := cache.DefaultRedisConfig().WithKeyPrefix("users:")

    c := cache.NewHybridCache[User](memConfig, client, redisConfig)
    c.AddIndex("email", func(u User) string { return u.Email })

    // Writes memory first, then Redis.
    if err := c.Set([]User{{ID: "1", Email: "alice@example.com"}}); err != nil {
        panic(err)
    }

    user, ok := c.GetByIndex("email", "alice@example.com") // served from memory
    _, _ = user, ok

    c.LoadFromRedis() // warm memory on startup
    c.SyncToRedis()   // push memory to Redis
}
```

## Change Detection

`GetHash()` returns a content hash of everything in the cache. Two caches
holding equal values produce equal hashes, in this process and in the next one.

The default hash walks each value with reflection and includes **every exported
field**, whatever its struct tags say:

```go
type User struct {
    ID       string
    Email    string
    Password string `json:"-"` // excluded from JSON, still hashed
}
```

That matters because a value whose `Password` changed is observably different
through `Get` and `GetAll`; a hash that skipped it would report "no change"
for a change that happened.

Equally, the hash never looks at memory addresses, so `MemoryCache[*User]`
hashes its contents rather than its layout — the same data hashes the same
after a restart or a reallocation. `time.Time` hashes by instant, so a
monotonic reading does not make two equal timestamps differ.

### When you need your own `HashFunc`

Supply one with `WithHashFunc` for:

- **Values with sensitive fields.** The default includes passwords and tokens in
  the hash input. Hash only the stable, non-sensitive fields instead.
- **Types whose identity lives in unexported fields, channels or funcs.** Those
  contribute only their type to the default hash.
- **Maps keyed by pointers, channels, or interfaces holding either.** Go
  compares such keys by identity while a content hash can only see what they
  point at. Given two distinct `*int` both addressing `1`, these two maps differ
  observably — `m[a]` is `"x"` in one and `"y"` in the other — yet no function of
  content alone can separate them:

  ```go
  map[*int]string{a: "x", b: "y"}
  map[*int]string{a: "y", b: "x"}
  ```

  Hashing the addresses would distinguish them and make the digest differ
  between runs, which this hash must never do.
- **Maps keyed by `time.Time`, at any depth.** Its `==` compares the `*Location`
  pointer and the monotonic reading, not only the instant, so `t.UTC()` and
  `t.In(time.FixedZone("z", 0))` are two distinct keys that can coexist in one
  map. Both a location pointer and a monotonic reading are process-local, so a
  reproducible hash cannot follow either.

```go
config := cache.DefaultConfig[User]().
    WithPrimaryKey(func(u User) string { return u.ID }).
    WithHashFunc(func(users []User) string {
        h := sha256.New()
        for _, u := range users { // callers control the order; see WithSortFunc
            fmt.Fprintf(h, "%s\x1f%s\x1e", u.ID, u.Email)
        }
        return hex.EncodeToString(h.Sum(nil))
    })
```

Use `WithSortFunc` (or `cache.StringSorter`) when the input order varies but the
hash must not.

## Index Key Collisions

Index keys are **normalized** — lower-cased and trimmed — before storage.
Primary keys are not. `Get("ABC")` and `GetByIndex("name", "ABC")` therefore
behave differently for the same string.

Normalization means two values differing only in case or surrounding whitespace
collide on one index key. The later value wins and the earlier becomes
unreachable through that index, so a lookup by email can return a different
record than the one intended. Set `OnIndexConflict` to find out:

```go
config := cache.DefaultConfig[User]().
    WithPrimaryKey(func(u User) string { return u.ID })

config.OnIndexConflict = func(indexName, key, existingPK, newPK string) {
    log.Printf("index %q key %q: %s is now shadowed by %s",
        indexName, key, existingPK, newPK)
}
```

The callback fires from both `Set` and `AddIndex` (so populating the cache
before adding an index is reported too), with the winner chosen by insertion
order. It runs with the cache lock released, so it may call back into `Get`,
`Len` or `GetAll`.

## Configuration

### Memory cache

**`PrimaryKeyFunc` is required** when storing non-empty data: `Set(non-empty)`
panics without it. `Set(nil)` and `Set([]V{})` are allowed either way.

```go
config := cache.DefaultConfig[User]().
    // Required: primary key extraction
    WithPrimaryKey(func(u User) string { return u.ID }).

    // Optional: custom hash (see Change Detection)
    WithHashFunc(myHash).

    // Optional: validation — invalid values are skipped
    WithValidateFunc(func(u User) error {
        if u.ID == "" {
            return fmt.Errorf("ID required")
        }
        return nil
    }).

    // Optional: normalization applied before storing
    WithNormalizeFunc(func(u User) User {
        u.Email = strings.ToLower(u.Email)
        return u
    }).

    // Optional: deterministic order for hashing
    WithSortFunc(cache.StringSorter(func(u User) string { return u.ID }))

// Optional: be told when two values share one index key
config.OnIndexConflict = func(indexName, key, existingPK, newPK string) { /* … */ }
```

| Option | Default | Notes |
|--------|---------|-------|
| `PrimaryKeyFunc` | `nil` | required for non-empty `Set` |
| `HashFunc` | reflection-based content hash | see Change Detection |
| `ValidateFunc` | `nil` | a value that fails is skipped, not an error |
| `NormalizeFunc` | `nil` | applied before storing and before key extraction |
| `SortFunc` | `nil` | without it, values hash in insertion order |
| `OnIndexConflict` | `nil` | without it, a shadowed index entry is silent |

### Redis cache

- `KeyPrefix` and `VersionKeySuffix` must be non-empty; `NewRedisCacheWithKey`
  requires a non-empty key. Use a **unique prefix per cache** to avoid
  collisions and key-space pollution.
- Both keys must be at most 512 bytes.
- Actual keys: data key = `KeyPrefix + "data"` (e.g. `myapp:cache:data`);
  version key = data key + `VersionKeySuffix` (e.g. `myapp:cache:data:version`).

```go
config := cache.DefaultRedisConfig().
    WithKeyPrefix("myapp:cache:").         // required, non-empty; default "cache:"
    WithVersionKeySuffix(":version").      // required, non-empty; default ":version"
    WithTTL(1 * time.Hour).                // applies to the DATA key; 0 means 1h at Set time
    WithOperationTimeout(5 * time.Second). // per-operation timeout
    WithMaxValueBytes(4 * 1024 * 1024)     // refuse an oversized value on Get; default 16MiB
```

| Option | Default | Notes |
|--------|---------|-------|
| `KeyPrefix` | `"cache:"` | make it unique per cache |
| `VersionKeySuffix` | `":version"` | appended to the data key |
| `TTL` | `1h` | data key only — the version key is persistent |
| `OperationTimeout` | `5s` | bounds each Redis round trip |
| `MaxValueBytes` | 16 MiB | `0` disables the check |

`MaxValueBytes` is enforced inside Redis: a Lua script measures the key with
`STRLEN` and returns the value in the same round trip, so an oversized value is
never transferred or allocated, and no other writer can swap the key between
the measurement and the read. `Get` returns an error naming both sizes.

`RedisCache` methods take no `context.Context`; each one builds its own from
`context.Background()` bounded by `OperationTimeout`. Caller cancellation and
trace context therefore do not reach Redis.

**`HybridCache.Set`** writes memory first, then Redis. If Redis fails, memory
already holds the new data — handle the error (retry, or call `LoadFromRedis`)
to reconcile.

## API Reference

### MemoryCache

```go
c := cache.NewMultiIndexCache[V](config)

// Index management
c.AddIndex(name, keyFunc)
c.RemoveIndex(name)
c.HasIndex(name) bool
c.IndexCount() int
c.IndexNames() []string

// Data operations
c.Set(values)
c.Get(primaryKey) (V, bool)
c.GetByIndex(indexName, key) (V, bool)
c.GetAll() []V
c.Len() int
c.Clear()

// Iteration over a snapshot; the callback may call back into the cache
c.Iterate(func(v V) bool)

// Change detection
c.GetHash() string
```

### RedisCache

```go
c := cache.NewRedisCache[V](client, config)
c := cache.NewRedisCacheWithKey[V](client, "custom:key", config)

c.Set(values) error
c.SetWithTTL(values, ttl) error
c.Get() ([]V, error)
c.Clear() error   // removes data and version key; GetVersion() then returns 0

c.Exists() (bool, error)
c.GetVersion() (int64, error)
c.TTL() (time.Duration, error)
c.Refresh() error // extends the data TTL; leaves the version key persistent
```

### HybridCache

```go
c := cache.NewHybridCache[V](memConfig, redisClient, redisConfig)

c.AddIndex(name, keyFunc)
c.Set(values) error
c.GetByIndex(indexName, key) (V, bool)
c.GetAll() []V

c.LoadFromRedis() error
c.SyncToRedis() error

c.Memory() *cache.MemoryCache[V]
c.Redis() *cache.RedisCache[V]
```

## Upgrade Notes (v1.7.0)

Dependency refresh only. No API was removed and no call needs rewriting.

- Test Redis is `miniredis` v2.39.0 (was v2.36.1).
- Transitive `yuin/gopher-lua` is v1.1.2 (was v1.1.1), matching the other kits.

## Upgrade Notes (v1.6.0)

Change detection and the Redis value guard both behave differently in this
release. No exported function was removed or changed shape; one config field was
added.

- **Hash values differ from earlier releases.** The default hash is computed a
  new way, so the first comparison after upgrading reports a change even when
  nothing changed. If you persist hashes across deploys, expect one spurious
  change and re-baseline.
- **`GetHash()` is now reproducible for pointer and `time.Time` values.** The
  default hash used `fmt.Sprintf("%v", v)`, which prints a pointer as its
  address: `MemoryCache[*User]` hashed its memory layout, so every restart and
  every reallocation reported a change that had not happened. `time.Time`'s
  monotonic reading had the same effect. Both are hashed by content now.
- **Fields hidden from JSON now participate in change detection.** An exported
  field tagged `json:"-"`, or state excluded by a custom `MarshalJSON`, used to
  be left out of the hash: two values differing only there hashed alike, so a
  real change went undetected. Every exported field is included now regardless
  of tags. If you relied on `json:"-"` to keep a volatile field out of the hash,
  move that exclusion into a `WithHashFunc`.
- **An empty cache has one hash.** A new cache started at `""` and `Clear()`
  reset to `""`, while `Set(nil)` computed the empty-state hash — so `Clear()` on
  an already-empty cache changed `GetHash()` and reported a phantom change. All
  three now agree.
- **`Iterate` no longer holds the read lock during the callback.** It
  snapshots under the lock and calls you without it, so a callback that touches
  the cache again no longer deadlocks, and a panicking callback no longer leaves
  the lock held. The callback now sees a snapshot rather than live data — the
  same guarantee `GetAll` already gave.
- **Shadowed index entries can be observed.** Set the new
  `Config.OnIndexConflict` to be told when two values normalize to one index key;
  previously the earlier value just became unreachable. `AddIndex` reports its
  rebuild collisions too, and picks the winner by insertion order rather than map
  iteration order.
- **Redis writes are atomic.** `Set`, `SetWithTTL` and `Clear` use
  `TxPipeline` (`MULTI`/`EXEC`) instead of a plain pipeline, so two concurrent
  writers can no longer leave one writer's data paired with another's version.
- **The Redis version key no longer expires.** It used to be given the data
  TTL; when it expired the counter restarted at 1, and a consumer tracking "is
  the version higher than what I last saw" stopped seeing updates. `Set` and
  `Refresh` now `PERSIST` it, which also repairs a version key written by an
  earlier release. `Clear` still deletes it explicitly.
- **`MaxValueBytes` actually prevents the allocation.** The size was checked
  after the value had been fetched — by the time `len(data)` could be compared,
  the oversized value was already in memory. A Lua script now measures and
  fetches in one atomic step.

## Use Cases

- **User whitelist caching**: O(1) lookups by phone, email or user ID
- **Configuration caching**: config reachable through several keys
- **Hot data caching**: frequently read data with multiple indexes
- **Distributed caching**: share state across instances through Redis

## Testing

```bash
go test ./...

# With coverage
go test ./... -coverprofile=coverage.out -covermode=atomic
go tool cover -func=coverage.out
```

## Contributing

Contributions are welcome — please open a Pull Request.

## License

Apache License 2.0 — see [LICENSE](LICENSE) for details.
