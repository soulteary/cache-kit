// Package loader adds request coalescing and stale refresh policies to ttlcache.
package loader

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/soulteary/cache-kit/v2/ttlcache"
)

// Policy controls how long successful, stale and failed loads are retained.
// TTLJitter is clamped to [0, 1] and varies FreshTTL in both directions.
type Policy struct {
	FreshTTL  time.Duration
	StaleTTL  time.Duration
	ErrorTTL  time.Duration
	TTLJitter float64
}

// Result describes a loaded or cached value.
type Result[V any] struct {
	Value  V
	Stale  bool
	Shared bool
}

// Config controls the loader's local cache and background refresh limit.
type Config struct {
	CleanupInterval time.Duration
	MaxEntries      int
	MaxRefresh      int
	Clock           ttlcache.Clock
}

type stored[V any] struct {
	value      V
	err        error
	freshUntil time.Time
}
type call[V any] struct {
	done  chan struct{}
	value V
	err   error
}

// Loader combines a Memory cache with same-key request coalescing and optional
// stale-while-revalidate behavior.
type Loader[K comparable, V any] struct {
	cache   *ttlcache.Memory[K, stored[V]]
	mu      sync.Mutex
	flights map[K]*call[V]
	refresh chan struct{}
	closed  bool
	clock   ttlcache.Clock
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New creates a Loader. MaxRefresh defaults to eight.
func New[K comparable, V any](cfg Config) *Loader[K, V] {
	if cfg.MaxRefresh <= 0 {
		cfg.MaxRefresh = 8
	}
	if cfg.Clock == nil {
		cfg.Clock = wallClock{}
	}
	cache := ttlcache.New(ttlcache.Config[K, stored[V]]{DefaultTTL: time.Minute, CleanupInterval: cfg.CleanupInterval, MaxEntries: cfg.MaxEntries, Clock: cfg.Clock})
	ctx, cancel := context.WithCancel(context.Background())
	return &Loader[K, V]{cache: cache, flights: make(map[K]*call[V]), refresh: make(chan struct{}, cfg.MaxRefresh), clock: cfg.Clock, ctx: ctx, cancel: cancel}
}

// GetOrLoad returns a cached value or calls fn. Concurrent misses for the same
// key share one call. A stale value is returned immediately while one refresh
// runs in the background.
func (l *Loader[K, V]) GetOrLoad(ctx context.Context, key K, fn func(context.Context, K) (V, error), p Policy) (Result[V], error) {
	if p.FreshTTL <= 0 {
		var zero Result[V]
		return zero, errors.New("loader: FreshTTL must be positive")
	}
	if cached, ok := l.cache.Get(key); ok {
		if cached.err != nil {
			var zero Result[V]
			return zero, cached.err
		}
		if l.clock.Now().Before(cached.freshUntil) {
			return Result[V]{Value: cached.value}, nil
		}
		if p.StaleTTL > 0 {
			l.refreshAsync(key, fn, p)
			return Result[V]{Value: cached.value, Stale: true}, nil
		}
	}
	return l.load(ctx, key, fn, p, false)
}

func (l *Loader[K, V]) load(ctx context.Context, key K, fn func(context.Context, K) (V, error), p Policy, background bool) (Result[V], error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		var z Result[V]
		return z, ttlcache.ErrClosed
	}
	if existing := l.flights[key]; existing != nil {
		l.mu.Unlock()
		return wait(ctx, existing, true)
	}
	// The first cache lookup happens before taking l.mu. A previous flight may
	// have populated the cache and removed itself while this caller waited for
	// the mutex, so check again before registering another backend request.
	if !background {
		if cached, ok := l.cache.Get(key); ok {
			if cached.err != nil {
				l.mu.Unlock()
				var zero Result[V]
				return zero, cached.err
			}
			if l.clock.Now().Before(cached.freshUntil) {
				l.mu.Unlock()
				return Result[V]{Value: cached.value}, nil
			}
			if p.StaleTTL > 0 {
				l.mu.Unlock()
				l.refreshAsync(key, fn, p)
				return Result[V]{Value: cached.value, Stale: true}, nil
			}
		}
	}
	c := &call[V]{done: make(chan struct{})}
	l.flights[key] = c
	l.wg.Add(1)
	loadCtx := l.ctx
	var release func()
	if !background {
		// Keep request-scoped values from the caller, but let the shared load
		// outlive that caller and tie its cancellation to the Loader instead.
		var cancel context.CancelFunc
		loadCtx, cancel = context.WithCancel(context.WithoutCancel(ctx))
		stop := context.AfterFunc(l.ctx, cancel)
		release = func() {
			stop()
			cancel()
		}
	}
	go l.execute(loadCtx, key, fn, p, background, c, release)
	l.mu.Unlock()
	return wait(ctx, c, false)
}

func wait[V any](ctx context.Context, c *call[V], shared bool) (Result[V], error) {
	select {
	case <-c.done:
		return Result[V]{Value: c.value, Shared: shared}, c.err
	case <-ctx.Done():
		var zero Result[V]
		return zero, ctx.Err()
	}
}

func (l *Loader[K, V]) execute(ctx context.Context, key K, fn func(context.Context, K) (V, error), p Policy, background bool, c *call[V], release func()) {
	defer l.wg.Done()
	if release != nil {
		defer release()
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				c.err = fmt.Errorf("loader panic: %v", r)
			}
		}()
		c.value, c.err = fn(ctx, key)
	}()
	now := l.clock.Now()
	if c.err == nil {
		fresh := jitter(p.FreshTTL, p.TTLJitter)
		total := fresh + p.StaleTTL
		c.err = l.cache.Set(key, stored[V]{value: c.value, freshUntil: now.Add(fresh)}, total)
	} else if p.ErrorTTL > 0 && !background {
		_ = l.cache.Set(key, stored[V]{err: c.err, freshUntil: now.Add(p.ErrorTTL)}, p.ErrorTTL)
	}
	l.mu.Lock()
	delete(l.flights, key)
	close(c.done)
	l.mu.Unlock()
}

func (l *Loader[K, V]) refreshAsync(key K, fn func(context.Context, K) (V, error), p Policy) {
	l.mu.Lock()
	if l.closed || l.flights[key] != nil {
		l.mu.Unlock()
		return
	}
	select {
	case l.refresh <- struct{}{}:
		c := &call[V]{done: make(chan struct{})}
		l.flights[key] = c
		l.wg.Add(1)
		go l.execute(l.ctx, key, fn, p, true, c, func() { <-l.refresh })
		l.mu.Unlock()
	default:
		l.mu.Unlock()
	}
}

// Close cancels background refreshes, waits for them and closes the local cache.
func (l *Loader[K, V]) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.cancel()
	l.wg.Wait()
	return l.cache.Close()
}

func jitter(d time.Duration, f float64) time.Duration {
	if f <= 0 {
		return d
	}
	if f > 1 {
		f = 1
	}
	factor := 1 - f + rand.Float64()*(2*f)
	return time.Duration(float64(d) * factor)
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
