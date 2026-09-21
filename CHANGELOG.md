# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Because Go encodes the major version in the import path, every major release
also changes the module path. The current one is
`github.com/soulteary/cache-kit/v2`.

## [Unreleased]

## [2.0.0] — 2026-09-21

Two breaking changes, released together. Each one on its own would force every
user to rewrite their import paths, so batching them costs one migration
instead of two.

### Changed — BREAKING

- **The Redis cache moved to the `rediscache` subpackage.** The root package no
  longer imports go-redis, so a binary that only caches in memory no longer
  links it. Measured for a program that imports the root package and nothing
  else, v1.7.0 against this release (`go build -trimpath`, Go 1.27.0,
  linux/amd64):

  | | v1.7.0 | v2.0.0 |
  |---|---|---|
  | Linked packages | 199 | 95 |
  | Modules in the build | 5 | 1 |
  | Binary size | 6,991,822 B | 3,991,817 B (−42.9%) |
  | Consumer `go.mod` indirect requires | 4 | 0 |
  | Consumer `go.sum` modules | 14 | 1 |

  The root package now depends on nothing outside the standard library, its
  tests included, so that program's own `go.mod` ends up with no `// indirect`
  requirement at all and thirteen modules leave its `go.sum`: go-redis and
  miniredis, plus the eleven they drag along.

  | Removed from the root package | Replacement |
  |---|---|
  | `cache.RedisCache[V]` | `rediscache.Cache[V]` |
  | `cache.NewRedisCache[V]` | `rediscache.New[V]` |
  | `cache.NewRedisCacheWithKey[V]` | `rediscache.NewWithKey[V]` |
  | `cache.RedisConfig` | `rediscache.Config` |
  | `cache.DefaultRedisConfig` | `rediscache.DefaultConfig` |
  | `cache.HybridCache[V]` | `rediscache.Hybrid[V]` |
  | `cache.NewHybridCache[V]` | `rediscache.NewHybrid[V]` |

  Keeping deprecated shims in the root package was not an option: a shim has to
  import go-redis, which relinks it and gives back the entire benefit.

  A subpackage is enough; go-redis does not need its own module. Module graph
  pruning keeps a requirement that no imported package needs out of the
  consumer's `go.mod` and `go.sum` entirely — the measurement above is of a
  consumer resolved against a real v2.0.0 module, not a `replace`.

  What pruning does **not** do is insulate a consumer from minimal version
  selection: a program that uses go-redis itself at v9.7.0 and depends on
  cache-kit v2 resolves to v9.22.0, the version this module requires.

  **The module path is therefore now `github.com/soulteary/cache-kit/v2`**, by
  the import compatibility rule. Every user must update the import path,
  including programs with no Redis at all, which are otherwise unaffected.

- **Every Redis operation takes a `context.Context`.** Each method used to root
  its call at `context.Background()`, so a caller that had already given up — a
  cancelled HTTP request, a worker shutting down — could not stop the round
  trip, and no trace context ever reached the server. The code said so and left
  it alone, because fixing it meant a parameter on eight exported methods. This
  is the release where those signatures can change; deferring it would have
  meant a v3 that breaks every import path again for this alone.

  | v1 | v2 |
  |---|---|
  | `Cache.Set(values)` | `Set(ctx, values)` |
  | `Cache.SetWithTTL(values, ttl)` | `SetWithTTL(ctx, values, ttl)` |
  | `Cache.Get()` | `Get(ctx)` |
  | `Cache.Exists()` | `Exists(ctx)` |
  | `Cache.GetVersion()` | `GetVersion(ctx)` |
  | `Cache.Clear()` | `Clear(ctx)` |
  | `Cache.TTL()` | `TTL(ctx)` |
  | `Cache.Refresh()` | `Refresh(ctx)` |
  | `Hybrid.Set(values)` | `Set(ctx, values)` |
  | `Hybrid.LoadFromRedis()` | `LoadFromRedis(ctx)` |
  | `Hybrid.SyncToRedis()` | `SyncToRedis(ctx)` |

  `Hybrid`'s memory-only methods — `AddIndex`, `GetByIndex`, `GetAll`,
  `Memory`, `Redis` — are unchanged: they issue no commands.

- `OperationTimeout` now bounds an operation on top of the deadline the
  caller's context already carries, instead of replacing it.

- Nothing else changed. The memory cache, the index machinery, the content hash
  and their behaviour are the same as v1.7.0, apart from the `/v2` in the
  import path.

### Added

- The `rediscache` subpackage. `rediscache.Client` is the part of a go-redis
  client this package uses, so `*redis.Client`, `*redis.ClusterClient`,
  `*redis.Ring` and `redis.UniversalClient` all work where the root package
  took only `*redis.Client` — a Cluster or Sentinel deployment no longer needs
  a cache of its own. On a sharded client the data and version keys need a
  common hash tag, since `Set`, `SetWithTTL` and `Clear` write both in one
  transaction and Redis refuses a `MULTI` spanning slots; this is documented on
  the interface.

  Taking an interface reopens a hole the `*redis.Client` field closed by
  construction, so the constructors close it explicitly: a plain `client == nil`
  misses a typed nil, such as an unassigned `*redis.Client` field, and issuing
  a command on one panics where every method used to return `redis client is
  nil`. The check goes through `reflect` and keeps returning the error.

- `CHANGELOG.md`.

- `doc.go` in the root package, describing the layout and where Redis went.

- Runnable examples for both packages (`Example`,
  `ExampleMemoryCache_GetByIndex`, `ExampleMemoryCache_GetHash`,
  `ExampleConfig_WithSortFunc`, `ExampleCache_Get`, `ExampleHybrid`) that
  `go test` verifies, so they cannot drift from the API.

- `.github/workflows/release.yml`. Nine tags exist with nothing having checked
  any of them, and the mistake that matters at tag time is caught then or not
  at all: a `vN` tag on a module path without the matching `/vN` suffix is
  unfetchable, and the first report comes from a user who cannot `go get` the
  release. This module becomes `/v2` here, which is when that becomes possible
  to get wrong. It runs on a `v*` tag (and on demand): the module path must
  carry the tag's major version, with v0 and v1 taking no suffix, and both
  READMEs' `go get` line must name that same path. Then the CI gate against the
  tagged commit — gofmt, `go mod tidy` cleanliness, vet, golangci-lint,
  `go test -race` with coverage, and govulncheck. Verification only: it
  publishes nothing and takes no write permissions.

- A CI job asserting that the root package reaches no module but this one. The
  split is only worth anything while that holds, nothing else checks it, and a
  single careless import would put go-redis back into every user's binary with
  no signal but a bigger download.

- `.github/dependabot.yml`. Weekly gomod and github-actions updates, minor and
  patch grouped into one PR, majors left separate — for this module a
  dependency major is a judgement call. The release gate is an action too, so a
  silently stale action would be a stale release check.

### Fixed

- A non-positive `OperationTimeout` meant an already-expired context, so a
  `RedisConfig` built by hand rather than from `DefaultRedisConfig` failed every
  operation before it was issued. It now means this package adds no bound of its
  own and the caller's context stands as given.

## [1.7.0] — 2026-09-14

### Changed

- Dependency refresh only; no API change. `miniredis` v2.36.1 → v2.39.0, and
  the transitive `yuin/gopher-lua` v1.1.1 → v1.1.2, matching the other kits.

## [1.6.0] — 2026-09-12

### Changed — BREAKING (behaviour)

No exported function was removed or changed shape, but change detection and the
Redis value guard both behave differently.

- Hash values differ from earlier releases: the first comparison after
  upgrading reports a change even when nothing changed. Re-baseline any
  persisted hash.
- Every exported field participates in the hash, whatever its struct tags say.
  State hidden by `json:"-"` or a custom `MarshalJSON` used to be left out, so a
  real change went undetected. Move a deliberate exclusion into a
  `WithHashFunc`.
- `Iterate` no longer holds the read lock during the callback; it snapshots
  under the lock and calls you without it.

### Fixed

- `GetHash()` is reproducible for pointer and `time.Time` values. The default
  hash used `fmt.Sprintf("%v", v)`, which prints a pointer as its address, so
  `MemoryCache[*User]` hashed its memory layout and reported a change on every
  restart. `time.Time`'s monotonic reading had the same effect.
- A new cache, `Clear()` and `Set(nil)` now agree on one empty-state hash;
  `Clear()` on an already-empty cache used to report a phantom change.
- Redis `Set`, `SetWithTTL` and `Clear` use `TxPipeline` (`MULTI`/`EXEC`), so
  two concurrent writers can no longer leave one writer's data paired with
  another's version.
- The Redis version key no longer expires. It used to take the data TTL; when
  it expired the counter restarted at 1 and a consumer tracking "is the version
  higher than what I last saw" stopped seeing updates.
- `MaxValueBytes` prevents the allocation instead of just the unmarshal. A Lua
  script measures and fetches in one atomic step.

### Added

- `Config.OnIndexConflict`, reporting two values that normalize to one index
  key. `AddIndex` reports its rebuild collisions too, and picks the winner by
  insertion order rather than map iteration order.

## [1.5.0] and earlier

Predate this file; see the git history.

[Unreleased]: https://github.com/soulteary/cache-kit/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/soulteary/cache-kit/compare/v1.7.0...v2.0.0
[1.7.0]: https://github.com/soulteary/cache-kit/compare/v1.6.0...v1.7.0
[1.6.0]: https://github.com/soulteary/cache-kit/compare/v1.5.0...v1.6.0
