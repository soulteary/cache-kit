# cache-kit

A Go library for thread-safe, multi-index memory caching with Redis support.

## Features

- **Multi-Index Lookup**: O(1) lookups by multiple keys (e.g., ID, email, phone)
- **Thread-Safe**: Safe for concurrent read/write access
- **Hash-Based Change Detection**: Detect cache changes efficiently
- **Redis Support**: Redis cache adapter for distributed scenarios
- **Hybrid Cache**: Combine memory and Redis for optimal performance
- **Generic Types**: Works with any data type using Go generics
- **Fluent Configuration**: Builder pattern for easy configuration

## Installation

```bash
go get github.com/soulteary/cache-kit
```

## Quick Start

### Memory Cache with Multi-Index

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
    // Create cache with primary key
    config := cache.DefaultConfig[User]().
        WithPrimaryKey(func(u User) string { return u.ID })

    c := cache.NewMultiIndexCache(config)

    // Add indexes for fast lookup by email and phone
    c.AddIndex("email", func(u User) string { return u.Email })
    c.AddIndex("phone", func(u User) string { return u.Phone })

    // Set data
    users := []User{
        {ID: "1", Email: "alice@example.com", Phone: "1111111111", Name: "Alice"},
        {ID: "2", Email: "bob@example.com", Phone: "2222222222", Name: "Bob"},
    }
    c.Set(users)

    // Lookup by primary key (O(1))
    user, ok := c.Get("1")
    if ok {
        fmt.Println("Found by ID:", user.Name)
    }

    // Lookup by email index (O(1))
    user, ok = c.GetByIndex("email", "bob@example.com")
    if ok {
        fmt.Println("Found by email:", user.Name)
    }

    // Lookup by phone index (O(1))
    user, ok = c.GetByIndex("phone", "1111111111")
    if ok {
        fmt.Println("Found by phone:", user.Name)
    }

    // Get hash for change detection
    fmt.Println("Cache hash:", c.GetHash())
}
```

### Redis Cache

```go
package main

import (
    "github.com/redis/go-redis/v9"
    cache "github.com/soulteary/cache-kit"
)

func main() {
    client := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })
    defer client.Close()

    // Create Redis cache
    config := cache.DefaultRedisConfig().
        WithKeyPrefix("myapp:users:").
        WithTTL(30 * time.Minute)

    c := cache.NewRedisCache[User](client, config)

    // Set data
    users := []User{{ID: "1", Name: "Alice"}}
    if err := c.Set(users); err != nil {
        panic(err)
    }

    // Get data
    users, err := c.Get()
    if err != nil {
        panic(err)
    }

    // Check version (for cache invalidation)
    version, _ := c.GetVersion()
    fmt.Println("Cache version:", version)
}
```

### Hybrid Cache (Memory + Redis)

```go
package main

import (
    "github.com/redis/go-redis/v9"
    cache "github.com/soulteary/cache-kit"
)

func main() {
    client := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })
    defer client.Close()

    // Create hybrid cache
    memConfig := cache.DefaultConfig[User]().
        WithPrimaryKey(func(u User) string { return u.ID })
    redisConfig := cache.DefaultRedisConfig().WithKeyPrefix("users:")

    c := cache.NewHybridCache[User](memConfig, client, redisConfig)

    // Add indexes
    c.AddIndex("email", func(u User) string { return u.Email })

    // Set stores in both memory and Redis
    c.Set([]User{{ID: "1", Email: "alice@example.com"}})

    // Fast lookup from memory
    user, ok := c.GetByIndex("email", "alice@example.com")

    // Load from Redis on startup
    c.LoadFromRedis()

    // Sync memory to Redis
    c.SyncToRedis()
}
```

## Configuration

### Memory Cache Config

```go
config := cache.DefaultConfig[User]().
    // Required: Primary key extraction
    WithPrimaryKey(func(u User) string { return u.ID }).

    // Optional: Custom hash function
    WithHashFunc(func(users []User) string {
        // Your custom hash logic
        return "custom-hash"
    }).

    // Optional: Validation (skip invalid items)
    WithValidateFunc(func(u User) error {
        if u.ID == "" {
            return fmt.Errorf("ID required")
        }
        return nil
    }).

    // Optional: Normalization (transform before storing)
    WithNormalizeFunc(func(u User) User {
        u.Email = strings.ToLower(u.Email)
        return u
    }).

    // Optional: Sort function for deterministic hashing
    WithSortFunc(cache.StringSorter(func(u User) string {
        return u.ID
    }))
```

### Redis Config

```go
config := cache.DefaultRedisConfig().
    WithKeyPrefix("myapp:cache:").    // Key prefix
    WithVersionKeySuffix(":version"). // Version key suffix
    WithTTL(1 * time.Hour).           // Cache TTL
    WithOperationTimeout(5 * time.Second) // Operation timeout
```

## API Reference

### MemoryCache

```go
// Create cache
cache := NewMultiIndexCache[V](config)

// Index management
cache.AddIndex(name, keyFunc)
cache.RemoveIndex(name)
cache.HasIndex(name) bool
cache.IndexCount() int
cache.IndexNames() []string

// Data operations
cache.Set(values)
cache.Get(primaryKey) (V, bool)
cache.GetByIndex(indexName, key) (V, bool)
cache.GetAll() []V
cache.Len() int
cache.Clear()

// Iteration
cache.Iterate(func(v V) bool)

// Change detection
cache.GetHash() string
```

### RedisCache

```go
// Create cache
cache := NewRedisCache[V](client, config)
cache := NewRedisCacheWithKey[V](client, "custom:key", config)

// Data operations
cache.Set(values) error
cache.SetWithTTL(values, ttl) error
cache.Get() ([]V, error)
cache.Clear() error

// Status
cache.Exists() (bool, error)
cache.GetVersion() (int64, error)
cache.TTL() (time.Duration, error)
cache.Refresh() error
```

### HybridCache

```go
// Create cache
cache := NewHybridCache[V](memConfig, redisClient, redisConfig)

// Index management
cache.AddIndex(name, keyFunc)

// Data operations
cache.Set(values) error
cache.GetByIndex(indexName, key) (V, bool)
cache.GetAll() []V

// Sync operations
cache.LoadFromRedis() error
cache.SyncToRedis() error

// Access underlying caches
cache.Memory() *MemoryCache[V]
cache.Redis() *RedisCache[V]
```

## Use Cases

- **User whitelist caching**: Fast O(1) lookups by phone, email, or user ID
- **Configuration caching**: Cache config with multi-key access
- **Hot data caching**: Frequently accessed data with multiple indexes
- **Distributed caching**: Share cache across instances with Redis

## License

Apache License 2.0
