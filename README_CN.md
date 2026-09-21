# cache-kit

[![Go Reference](https://pkg.go.dev/badge/github.com/soulteary/cache-kit/v2.svg)](https://pkg.go.dev/github.com/soulteary/cache-kit/v2)
[![Go Report Card](.github/goreportcard.svg)](.github/goreportcard-report.md)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![codecov](https://codecov.io/gh/soulteary/cache-kit/graph/badge.svg)](https://codecov.io/gh/soulteary/cache-kit)

[English](README.md)

线程安全的多索引内存缓存库，支持 Redis。可以用任意多个键做 O(1) 查找，用可复现的
哈希做内容变更检测，并通过 Redis 在多实例间共享缓存。

根包只依赖标准库。Redis 放在 `rediscache` 子包里，所以只用内存缓存的程序根本不会
链接 go-redis：链接包数 95 而不是 199，二进制 3.99 MB 而不是 6.99 MB。详见
[依赖体积](#依赖体积)。

## 特性

- **多索引查找**：按多个键（ID、邮箱、手机号……）做 O(1) 查找
- **线程安全**：并发读写安全
- **可复现的变更检测**：跨进程稳定的内容哈希
- **Redis 支持**：`./rediscache` 子包里的 Redis 适配器，用到才导入
- **混合缓存**：内存在前，Redis 在后
- **根包零第三方依赖**：不导入 `rediscache`，Redis 就一分钱不花
- **泛型**：基于 Go 泛型，适用于任意值类型
- **链式配置**：两套配置都支持构造器风格

## 要求

- **Go 1.27+**（`go.mod` 声明 `go 1.27.0`）
- **仅当**导入 `./rediscache` 时才需要 `github.com/redis/go-redis/v9`；根包什么都不需要
- 设置了 `MaxValueBytes` 时，Redis 需支持 `EVAL`（2.6+ 均可）

## 安装

```bash
go get github.com/soulteary/cache-kit/v2
```

从 v1 升级？见 [升级说明（v2.0.0）](#升级说明v200)。

## 快速开始

### 带多索引的内存缓存

```go
package main

import (
    "fmt"

    cache "github.com/soulteary/cache-kit/v2"
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

    user, ok := c.Get("1")                                  // 按主键
    user, ok = c.GetByIndex("email", "bob@example.com")     // 按索引
    user, ok = c.GetByIndex("phone", "1111111111")
    _ = user
    _ = ok

    fmt.Println("缓存哈希:", c.GetHash())
}
```

### Redis 缓存

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/soulteary/cache-kit/v2/rediscache"
)

func main() {
    ctx := context.Background()

    client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
    defer client.Close()

    config := rediscache.DefaultConfig().
        WithKeyPrefix("myapp:users:").
        WithTTL(30 * time.Minute)

    c := rediscache.New[User](client, config)

    if err := c.Set(ctx, []User{{ID: "1", Name: "Alice"}}); err != nil {
        panic(err)
    }

    users, err := c.Get(ctx)
    if err != nil {
        panic(err)
    }
    _ = users

    // 单调递增计数器，用来回答"是否有别人写过"。
    version, _ := c.GetVersion(ctx)
    fmt.Println("缓存版本:", version)
}
```

### 混合缓存（内存 + Redis）

```go
package main

import (
    "context"

    "github.com/redis/go-redis/v9"
    cache "github.com/soulteary/cache-kit/v2"
    "github.com/soulteary/cache-kit/v2/rediscache"
)

func main() {
    ctx := context.Background()

    client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
    defer client.Close()

    memConfig := cache.DefaultConfig[User]().
        WithPrimaryKey(func(u User) string { return u.ID })
    redisConfig := rediscache.DefaultConfig().WithKeyPrefix("users:")

    c := rediscache.NewHybrid[User](memConfig, client, redisConfig)
    c.AddIndex("email", func(u User) string { return u.Email })

    // 先写内存，再写 Redis。
    if err := c.Set(ctx, []User{{ID: "1", Email: "alice@example.com"}}); err != nil {
        panic(err)
    }

    user, ok := c.GetByIndex("email", "alice@example.com") // 由内存响应
    _, _ = user, ok

    c.LoadFromRedis(ctx) // 启动时预热内存
    c.SyncToRedis(ctx)   // 把内存推到 Redis
}
```

## 变更检测

`GetHash()` 返回缓存内容的哈希。内容相等的两份缓存得到相同哈希——本进程如此，下个
进程也如此。

默认哈希用反射遍历每个值，包含**所有导出字段**，不管结构体 tag 怎么写：

```go
type User struct {
    ID       string
    Email    string
    Password string `json:"-"` // 不进 JSON，但参与哈希
}
```

这一点很重要：`Password` 变了的值，通过 `Get` 和 `GetAll` 是可以观察到差异的；
跳过它的哈希会对一个真实发生的变更报告"没变"。

同理，哈希不看内存地址，所以 `MemoryCache[*User]` 哈希的是内容而不是内存布局——
重启或重新分配之后，同样的数据哈希不变。`time.Time` 按时刻哈希，单调时钟读数不会让
两个相等的时间戳算作不同。

### 什么情况下需要自定义 `HashFunc`

以下场景请用 `WithHashFunc` 自己提供：

- **含敏感字段的值。** 默认哈希会把密码、令牌一起算进哈希输入。改为只哈希稳定、
  非敏感的字段。
- **标识位于未导出字段、channel 或 func 的类型。** 这些在默认哈希里只贡献类型信息。
- **以指针、channel，或持有二者的 interface 作为键的 map。** Go 按键的**身份**比较，
  而内容哈希只能看到它们指向的东西。给定两个都指向 `1` 的不同 `*int`，下面两个 map
  是可观察地不同的——`m[a]` 一个是 `"x"`、另一个是 `"y"`——但任何只看内容的函数都无法
  区分它们：

  ```go
  map[*int]string{a: "x", b: "y"}
  map[*int]string{a: "y", b: "x"}
  ```

  改为哈希地址能区分，但会让同一程序每次运行的摘要都不同，而这是本哈希绝不能做的事。
- **以 `time.Time` 作为键的 map（任意层级）。** 它的 `==` 比较的是 `*Location`
  指针和单调读数，不只是时刻，所以 `t.UTC()` 和 `t.In(time.FixedZone("z", 0))` 是可以
  共存于一个 map 的两个不同键。位置指针和单调读数都是进程内局部的，可复现的哈希
  无法追随它们。

```go
config := cache.DefaultConfig[User]().
    WithPrimaryKey(func(u User) string { return u.ID }).
    WithHashFunc(func(users []User) string {
        h := sha256.New()
        for _, u := range users { // 顺序由调用方控制，见 WithSortFunc
            fmt.Fprintf(h, "%s\x1f%s\x1e", u.ID, u.Email)
        }
        return hex.EncodeToString(h.Sum(nil))
    })
```

输入顺序会变、但哈希必须稳定时，请用 `WithSortFunc`（或 `cache.StringSorter`）。

## 索引键冲突

索引键在存储前会被**归一化**——转小写并去首尾空白。主键不会。因此对同一个字符串，
`Get("ABC")` 和 `GetByIndex("name", "ABC")` 行为并不相同。

归一化意味着只差大小写或首尾空白的两个值会落在同一个索引键上。后写入的值胜出，
先前那个通过该索引就不可达了——于是按邮箱查可能返回并非你想要的那条记录。设置
`OnIndexConflict` 就能知道：

```go
config := cache.DefaultConfig[User]().
    WithPrimaryKey(func(u User) string { return u.ID })

config.OnIndexConflict = func(indexName, key, existingPK, newPK string) {
    log.Printf("索引 %q 键 %q：%s 已被 %s 遮蔽",
        indexName, key, existingPK, newPK)
}
```

`Set` 和 `AddIndex` 都会触发这个回调（因此先灌数据再加索引的顺序也会被报告），
胜出者按插入顺序确定。回调执行时缓存锁已释放，所以可以回调 `Get`、`Len`、`GetAll`。

## 配置

### 内存缓存

存非空数据时**必须设置 `PrimaryKeyFunc`**：没设置时 `Set(非空)` 会 panic。
`Set(nil)` 和 `Set([]V{})` 两种情况下都允许。

```go
config := cache.DefaultConfig[User]().
    // 必填：主键提取
    WithPrimaryKey(func(u User) string { return u.ID }).

    // 可选：自定义哈希（见"变更检测"）
    WithHashFunc(myHash).

    // 可选：校验 —— 不合法的值会被跳过
    WithValidateFunc(func(u User) error {
        if u.ID == "" {
            return fmt.Errorf("ID required")
        }
        return nil
    }).

    // 可选：存储前归一化
    WithNormalizeFunc(func(u User) User {
        u.Email = strings.ToLower(u.Email)
        return u
    }).

    // 可选：为哈希提供确定的顺序
    WithSortFunc(cache.StringSorter(func(u User) string { return u.ID }))

// 可选：两个值落到同一索引键时收到通知
config.OnIndexConflict = func(indexName, key, existingPK, newPK string) { /* … */ }
```

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `PrimaryKeyFunc` | `nil` | 非空 `Set` 必填 |
| `HashFunc` | 基于反射的内容哈希 | 见"变更检测" |
| `ValidateFunc` | `nil` | 未通过的值被跳过，不报错 |
| `NormalizeFunc` | `nil` | 在存储与提取键之前执行 |
| `SortFunc` | `nil` | 不设置时按插入顺序哈希 |
| `OnIndexConflict` | `nil` | 不设置时，被遮蔽的索引项无声无息 |

### Redis 缓存

- `KeyPrefix` 和 `VersionKeySuffix` 必须非空；`rediscache.NewWithKey` 要求 key 非空。
  **每个缓存用独立前缀**，避免键冲突和键空间污染。
- 两个键都不得超过 512 字节。
- 实际键：数据键 = `KeyPrefix + "data"`（如 `myapp:cache:data`）；
  版本键 = 数据键 + `VersionKeySuffix`（如 `myapp:cache:data:version`）。

```go
config := rediscache.DefaultConfig().
    WithKeyPrefix("myapp:cache:").         // 必填非空，默认 "cache:"
    WithVersionKeySuffix(":version").      // 必填非空，默认 ":version"
    WithTTL(1 * time.Hour).                // 作用于数据键；0 表示 Set 时按 1h
    WithOperationTimeout(5 * time.Second). // 在你传入的 context 之上再加一层超时
    WithMaxValueBytes(4 * 1024 * 1024)     // Get 时拒绝超大值，默认 16MiB
```

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `KeyPrefix` | `"cache:"` | 每个缓存保持唯一 |
| `VersionKeySuffix` | `":version"` | 追加在数据键之后 |
| `TTL` | `1h` | 只作用于数据键——版本键是持久的 |
| `OperationTimeout` | `5s` | 在你的 context 之上约束每次往返；`0` 表示本包不额外加限制 |
| `MaxValueBytes` | 16 MiB | `0` 表示不检查 |

`MaxValueBytes` 在 Redis 内部生效：一段 Lua 脚本用 `STRLEN` 量出大小，并在同一次
往返里返回值，所以超大值既不会被传输也不会被分配，也不会有别的写入方在"量"和"读"
之间替换这个键。`Get` 返回的错误会同时给出两个尺寸。

所有访问 Redis 的方法都接收 `context.Context`，取消信号和追踪上下文因此能传到
服务端。`OperationTimeout` 是在你传入的 context 之上收紧，而不是替换它；设成 `0`
就完全听你自己的 deadline。

**`Hybrid.Set`** 先写内存、再写 Redis。Redis 失败时内存已经是新数据——请处理
这个错误（重试，或调用 `LoadFromRedis`）来对齐两边。

**Cluster、Ring 与 Sentinel。** 构造函数接收 `rediscache.Client`——也就是本包实际
用到的那几个命令——所以 `*redis.Client`、`*redis.ClusterClient`、`*redis.Ring` 和
`redis.UniversalClient` 都能直接传进来。用分片客户端时，请让数据键和版本键带同一个
hash tag（`KeyPrefix` 写 `"{users}:"` 而不是 `"users:"`）：`Set`、`SetWithTTL` 和
`Clear` 在一个事务里写这两个键，而 Redis 不接受跨 slot 的 `MULTI`。

## 依赖体积

Redis 放在 `./rediscache` 里，只有导入它的程序才会链接。以一个只导入根包的程序为准，
v1.7.0 与 v2.0.0 的实测对比（`go build -trimpath`，Go 1.27.0，linux/amd64）：

| | v1.7.0 | v2.0.0 |
|---|---|---|
| 链接包数 | 199 | 95 |
| 参与构建的模块数 | 5 | 1 |
| 二进制大小 | 6,991,822 B | 3,991,817 B（**−42.9%**） |
| 使用方 `go.mod` 的 indirect 条目 | 4 | 0 |
| 使用方 `go.sum` 的模块数 | 14 | 1 |

`go.sum` 这一行是 module graph pruning 的效果：没有任何被导入的包需要的依赖，根本
不会进入你的校验和文件。导入 `./rediscache` 会把 go-redis、`cespare/xxhash`、
`go.uber.org/atomic` 和 `golang.org/x/sys` 带回来——这是这个功能的成本，由真正用到
它的程序来付。

有一点 pruning 管不了：MVS（最小版本选择）。如果你的程序自己也用 go-redis，本模块的
依赖声明仍然会把它顶上去——一个锁定 `v9.7.0` 又依赖 cache-kit v2 的程序，最终解析到
`v9.22.0`。

## API 参考

### MemoryCache

```go
c := cache.NewMultiIndexCache[V](config)

// 索引管理
c.AddIndex(name, keyFunc)
c.RemoveIndex(name)
c.HasIndex(name) bool
c.IndexCount() int
c.IndexNames() []string

// 数据操作
c.Set(values)
c.Get(primaryKey) (V, bool)
c.GetByIndex(indexName, key) (V, bool)
c.GetAll() []V
c.Len() int
c.Clear()

// 基于快照遍历；回调内可以再调用缓存方法
c.Iterate(func(v V) bool)

// 变更检测
c.GetHash() string
```

### rediscache.Cache

```go
c := rediscache.New[V](client, config)
c := rediscache.NewWithKey[V](client, "custom:key", config)

c.Set(ctx, values) error
c.SetWithTTL(ctx, values, ttl) error
c.Get(ctx) ([]V, error)
c.Clear(ctx) error   // 同时删除数据键与版本键；之后 GetVersion() 返回 0

c.Exists(ctx) (bool, error)
c.GetVersion(ctx) (int64, error)
c.TTL(ctx) (time.Duration, error)
c.Refresh(ctx) error // 延长数据键 TTL；版本键保持持久
```

### rediscache.Hybrid

```go
c := rediscache.NewHybrid[V](memConfig, redisClient, redisConfig)

c.AddIndex(name, keyFunc)
c.Set(ctx, values) error
c.GetByIndex(indexName, key) (V, bool)   // 只走内存，不产生往返
c.GetAll() []V                           // 只走内存，不产生往返

c.LoadFromRedis(ctx) error
c.SyncToRedis(ctx) error

c.Memory() *cache.MemoryCache[V]
c.Redis() *rediscache.Cache[V]
```

## 升级说明（v2.0.0）

两个破坏性改动一起发布，这样只需要改一次 import 路径，而不是两次。

**1. Redis 缓存移到了 `./rediscache`。** 根包不再导入 go-redis，省下多少见
[依赖体积](#依赖体积)。根包里留兼容 shim 是行不通的——shim 必须 import go-redis，
一 import 就又链接回来，收益全没了。

因此模块路径变成 `github.com/soulteary/cache-kit/v2`。所有人都要改 import 路径，
包括完全不用 Redis 的程序。

| v1 | v2 |
|---|---|
| `cache.RedisCache[V]` | `rediscache.Cache[V]` |
| `cache.NewRedisCache[V]` | `rediscache.New[V]` |
| `cache.NewRedisCacheWithKey[V]` | `rediscache.NewWithKey[V]` |
| `cache.RedisConfig` | `rediscache.Config` |
| `cache.DefaultRedisConfig` | `rediscache.DefaultConfig` |
| `cache.HybridCache[V]` | `rediscache.Hybrid[V]` |
| `cache.NewHybridCache[V]` | `rediscache.NewHybrid[V]` |

内存缓存部分——`MemoryCache`、`Config`、`DefaultConfig`、`NewMultiIndexCache`、
`StringSorter`，以及哈希和索引机制——除了 import 路径里的 `/v2`，没有任何变化。

**2. 每个 Redis 操作都要传 `context.Context`。** v1 的每次调用都从
`context.Background()` 起头，于是调用方取消了也停不下这次往返，追踪上下文也到不了
服务端。这个问题不改签名就修不了，而改签名只能在大版本里做——就是这一版。

```go
c.Set(values)             → c.Set(ctx, values)
c.SetWithTTL(values, ttl) → c.SetWithTTL(ctx, values, ttl)
c.Get()                   → c.Get(ctx)
c.Exists()                → c.Exists(ctx)
c.GetVersion()            → c.GetVersion(ctx)
c.Clear()                 → c.Clear(ctx)
c.TTL()                   → c.TTL(ctx)
c.Refresh()               → c.Refresh(ctx)

h.Set(values)             → h.Set(ctx, values)
h.LoadFromRedis()         → h.LoadFromRedis(ctx)
h.SyncToRedis()           → h.SyncToRedis(ctx)
```

`Hybrid` 里只碰内存的方法——`AddIndex`、`GetByIndex`、`GetAll`、`Memory`、`Redis`
——签名不变，它们本来就不发命令。

随之而来两个行为变化：`OperationTimeout` 现在是在你传入的 context 之上收紧，而不是
替换它；`OperationTimeout` 为 0 或负数表示本包不额外加限制——在 v1 里它会得到一个
已经过期的 context，于是手写（而非用 `DefaultRedisConfig` 生成）的 `RedisConfig`
会让每次操作在发出之前就失败。

**v2 还有：** 构造函数接收 `rediscache.Client` 而不是 `*redis.Client`，所以
`*redis.ClusterClient`、`*redis.Ring` 和 `redis.UniversalClient` 不用包一层就能用。
传入具体类型的 nil 客户端仍然返回 `redis client is nil` 错误，而不是 panic。

## 升级说明（v1.7.0）

仅升级依赖。没有删除任何 API，调用方无需改代码。

- 测试用 Redis 为 `miniredis` v2.39.0（此前 v2.36.1）。
- 间接依赖 `yuin/gopher-lua` 升至 v1.1.2（此前 v1.1.1），与其他 kit 对齐。

## 升级说明（v1.6.0）

本次发布改变了变更检测与 Redis 值上限守卫的行为。没有删除或改签名的导出函数，新增了
一个配置字段。

- **哈希值与旧版本不同。** 默认哈希换了算法，所以升级后第一次比较即便内容没变也会
  报告"有变化"。如果你跨发布持久化哈希，请预期一次误报并重新建立基线。
- **指针与 `time.Time` 的 `GetHash()` 现在可复现。** 默认哈希此前用
  `fmt.Sprintf("%v", v)`，而 `%v` 把指针打成地址：`MemoryCache[*User]` 哈希的是内存
  布局，于是每次重启、每次重新分配都报告一次并未发生的变更。`time.Time` 的单调读数
  有同样的效果。现在两者都按内容哈希。
- **被 JSON 排除的字段现在参与变更检测。** 带 `json:"-"` 的导出字段，或被自定义
  `MarshalJSON` 排除的状态，此前不进哈希：只差这些字段的两个值哈希相同，于是真实
  变更被漏掉。现在所有导出字段一律计入，不看 tag。如果你本来靠 `json:"-"` 把易变
  字段挡在哈希外面，请把这个排除逻辑搬进 `WithHashFunc`。
- **空缓存只有一个哈希。** 新建缓存初始为 `""`，`Clear()` 也重置为 `""`，而
  `Set(nil)` 算出的是空态哈希——于是对一个本来就空的缓存调用 `Clear()` 会改变
  `GetHash()`，报告一次幻觉变更。现在三者一致。
- **`Iterate` 执行回调时不再持读锁。** 它在锁内取快照、在锁外调用你的函数，所以回调
  里再访问缓存不会死锁，回调 panic 也不会把锁留在手里。代价是回调看到的是快照而非
  实时数据——这与 `GetAll` 原本给的保证相同。
- **被遮蔽的索引项可被观察。** 设置新增的 `Config.OnIndexConflict`，即可在两个值
  归一化到同一索引键时收到通知；此前先写入的那个只是默默变得不可达。`AddIndex`
  重建索引时的冲突同样会上报，且胜出者按插入顺序而非 map 遍历顺序确定。
- **Redis 写入是原子的。** `Set`、`SetWithTTL`、`Clear` 改用 `TxPipeline`
  （`MULTI`/`EXEC`）而非普通 pipeline，因此两个并发写入方不会再出现"一方的数据配上
  另一方的版本号"。
- **Redis 版本键不再过期。** 此前它被赋予数据键的 TTL；一旦过期，计数从 1 重新开始，
  而按"版本号是否高于我上次看到的"来判断的消费者就再也看不到更新。现在 `Set` 和
  `Refresh` 都会 `PERSIST` 它，这同时修复了旧版本写下的版本键。`Clear` 仍会显式删除。
- **`MaxValueBytes` 真正阻止了内存分配。** 原来是取回值之后再检查尺寸——等到能比较
  `len(data)` 时，超大值已经在内存里了。现在由 Lua 脚本在一次原子操作里完成量测与
  取值。

## 使用场景

- **用户白名单缓存**：按手机号、邮箱或用户 ID 做 O(1) 查找
- **配置缓存**：配置可通过多个键访问
- **热数据缓存**：高频读取、需要多索引的数据
- **分布式缓存**：通过 Redis 在多实例间共享状态

## 测试

```bash
go test ./...

# 带覆盖率
go test ./... -coverprofile=coverage.out -covermode=atomic
go tool cover -func=coverage.out
```

## 贡献

欢迎贡献，直接发起 Pull Request 即可。

## 许可证

Apache License 2.0 —— 详见 [LICENSE](LICENSE)。
