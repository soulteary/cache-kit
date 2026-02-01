# cache-kit

[![Go Reference](https://pkg.go.dev/badge/github.com/soulteary/cache-kit.svg)](https://pkg.go.dev/github.com/soulteary/cache-kit)
[![Go Report Card](https://goreportcard.com/badge/github.com/soulteary/cache-kit)](https://goreportcard.com/report/github.com/soulteary/cache-kit)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![codecov](https://codecov.io/gh/soulteary/cache-kit/graph/badge.svg)](https://codecov.io/gh/soulteary/cache-kit)

[English](README.md)

一个支持多索引查询的线程安全内存缓存 Go 库，同时支持 Redis。

## 特性

- **多索引查询**: O(1) 时间复杂度的多键查询（如 ID、邮箱、手机号）
- **线程安全**: 支持并发读写
- **基于哈希的变更检测**: 高效检测缓存变化
- **Redis 支持**: Redis 缓存适配器，支持分布式场景
- **混合缓存**: 结合内存和 Redis 实现最佳性能
- **泛型支持**: 使用 Go 泛型，适用于任何数据类型
- **流式配置**: 构建器模式，配置简单

## 安装

```bash
go get github.com/soulteary/cache-kit
```

## 快速开始

### 带多索引的内存缓存

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
    // 创建带主键的缓存
    config := cache.DefaultConfig[User]().
        WithPrimaryKey(func(u User) string { return u.ID })

    c := cache.NewMultiIndexCache(config)

    // 添加索引，支持通过邮箱和手机号快速查询
    c.AddIndex("email", func(u User) string { return u.Email })
    c.AddIndex("phone", func(u User) string { return u.Phone })

    // 设置数据
    users := []User{
        {ID: "1", Email: "alice@example.com", Phone: "1111111111", Name: "Alice"},
        {ID: "2", Email: "bob@example.com", Phone: "2222222222", Name: "Bob"},
    }
    c.Set(users)

    // 通过主键查询 (O(1))
    user, ok := c.Get("1")
    if ok {
        fmt.Println("通过 ID 找到:", user.Name)
    }

    // 通过邮箱索引查询 (O(1))
    user, ok = c.GetByIndex("email", "bob@example.com")
    if ok {
        fmt.Println("通过邮箱找到:", user.Name)
    }

    // 通过手机号索引查询 (O(1))
    user, ok = c.GetByIndex("phone", "1111111111")
    if ok {
        fmt.Println("通过手机号找到:", user.Name)
    }

    // 获取哈希用于变更检测
    fmt.Println("缓存哈希:", c.GetHash())
}
```

### Redis 缓存

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

    // 创建 Redis 缓存
    config := cache.DefaultRedisConfig().
        WithKeyPrefix("myapp:users:").
        WithTTL(30 * time.Minute)

    c := cache.NewRedisCache[User](client, config)

    // 设置数据
    users := []User{{ID: "1", Name: "Alice"}}
    if err := c.Set(users); err != nil {
        panic(err)
    }

    // 获取数据
    users, err := c.Get()
    if err != nil {
        panic(err)
    }

    // 检查版本（用于缓存失效检测）
    version, _ := c.GetVersion()
    fmt.Println("缓存版本:", version)
}
```

### 混合缓存（内存 + Redis）

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

    // 创建混合缓存
    memConfig := cache.DefaultConfig[User]().
        WithPrimaryKey(func(u User) string { return u.ID })
    redisConfig := cache.DefaultRedisConfig().WithKeyPrefix("users:")

    c := cache.NewHybridCache[User](memConfig, client, redisConfig)

    // 添加索引
    c.AddIndex("email", func(u User) string { return u.Email })

    // Set 同时存储到内存和 Redis
    c.Set([]User{{ID: "1", Email: "alice@example.com"}})

    // 从内存快速查询
    user, ok := c.GetByIndex("email", "alice@example.com")

    // 启动时从 Redis 加载
    c.LoadFromRedis()

    // 将内存同步到 Redis
    c.SyncToRedis()
}
```

## 配置

### 内存缓存配置

```go
config := cache.DefaultConfig[User]().
    // 必需：主键提取函数
    WithPrimaryKey(func(u User) string { return u.ID }).

    // 可选：自定义哈希函数
    WithHashFunc(func(users []User) string {
        // 自定义哈希逻辑
        return "custom-hash"
    }).

    // 可选：验证函数（跳过无效项）
    WithValidateFunc(func(u User) error {
        if u.ID == "" {
            return fmt.Errorf("ID 不能为空")
        }
        return nil
    }).

    // 可选：规范化函数（存储前转换）
    WithNormalizeFunc(func(u User) User {
        u.Email = strings.ToLower(u.Email)
        return u
    }).

    // 可选：排序函数（用于确定性哈希）
    WithSortFunc(cache.StringSorter(func(u User) string {
        return u.ID
    }))
```

### Redis 配置

```go
config := cache.DefaultRedisConfig().
    WithKeyPrefix("myapp:cache:").    // 键前缀
    WithVersionKeySuffix(":version"). // 版本键后缀
    WithTTL(1 * time.Hour).           // 缓存 TTL
    WithOperationTimeout(5 * time.Second) // 操作超时
```

## API 参考

### MemoryCache

```go
// 创建缓存
cache := NewMultiIndexCache[V](config)

// 索引管理
cache.AddIndex(name, keyFunc)
cache.RemoveIndex(name)
cache.HasIndex(name) bool
cache.IndexCount() int
cache.IndexNames() []string

// 数据操作
cache.Set(values)
cache.Get(primaryKey) (V, bool)
cache.GetByIndex(indexName, key) (V, bool)
cache.GetAll() []V
cache.Len() int
cache.Clear()

// 迭代
cache.Iterate(func(v V) bool)

// 变更检测
cache.GetHash() string
```

### RedisCache

```go
// 创建缓存
cache := NewRedisCache[V](client, config)
cache := NewRedisCacheWithKey[V](client, "custom:key", config)

// 数据操作
cache.Set(values) error
cache.SetWithTTL(values, ttl) error
cache.Get() ([]V, error)
cache.Clear() error

// 状态
cache.Exists() (bool, error)
cache.GetVersion() (int64, error)
cache.TTL() (time.Duration, error)
cache.Refresh() error
```

### HybridCache

```go
// 创建缓存
cache := NewHybridCache[V](memConfig, redisClient, redisConfig)

// 索引管理
cache.AddIndex(name, keyFunc)

// 数据操作
cache.Set(values) error
cache.GetByIndex(indexName, key) (V, bool)
cache.GetAll() []V

// 同步操作
cache.LoadFromRedis() error
cache.SyncToRedis() error

// 访问底层缓存
cache.Memory() *MemoryCache[V]
cache.Redis() *RedisCache[V]
```

## 使用场景

- **用户白名单缓存**: 通过手机号、邮箱或用户 ID 进行 O(1) 快速查询
- **配置缓存**: 支持多键访问的配置缓存
- **热数据缓存**: 带多索引的高频访问数据
- **分布式缓存**: 通过 Redis 在多实例间共享缓存

## 许可证

Apache License 2.0
