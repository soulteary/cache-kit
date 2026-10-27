package ttlcache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPerKeyTTLAndNoExpiration(t *testing.T) {
	clock := NewManualClock(time.Unix(100, 0))
	c := New(Config[string, string]{DefaultTTL: time.Minute, Clock: clock})
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Set("short", "a", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("forever", "b", NoExpiration); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	if _, ok := c.Get("short"); ok {
		t.Fatal("expired value returned")
	}
	if got, ok := c.Get("forever"); !ok || got != "b" {
		t.Fatalf("permanent value = %q, %v", got, ok)
	}
}

func TestCapacityLRUAndCost(t *testing.T) {
	c := New(Config[string, string]{MaxEntries: 2, MaxCost: 4, Cost: func(_ string, v string) int64 { return int64(len(v)) }})
	t.Cleanup(func() { _ = c.Close() })
	_ = c.Set("a", "aa", NoExpiration)
	_ = c.Set("b", "bb", NoExpiration)
	_, _ = c.Get("a")
	_ = c.Set("c", "cc", NoExpiration)
	if _, ok := c.Get("b"); ok {
		t.Fatal("least recently used entry retained")
	}
	if c.Stats().Evictions != 1 {
		t.Fatalf("evictions = %d", c.Stats().Evictions)
	}
	if err := c.Set("large", "12345", NoExpiration); err != ErrItemTooLarge {
		t.Fatalf("large item error = %v", err)
	}
}

func TestEvictionCallbackRunsOutsideLock(t *testing.T) {
	var c *Memory[string, string]
	done := make(chan struct{})
	c = New(Config[string, string]{MaxEntries: 1, OnEvict: func(string, string, EvictionReason) { _ = c.Len(); close(done) }})
	t.Cleanup(func() { _ = c.Close() })
	_ = c.Set("a", "a", NoExpiration)
	_ = c.Set("b", "b", NoExpiration)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback deadlocked")
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New(DefaultConfig[int, int]())
	t.Cleanup(func() { _ = c.Close() })
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = c.Set(j, n, time.Minute)
				_, _ = c.Get(j)
				if j%11 == 0 {
					c.Delete(j)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestCloseIsIdempotent(t *testing.T) {
	c := New(DefaultConfig[string, string]())
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("x", "y", time.Second); err != ErrClosed {
		t.Fatalf("set after close = %v", err)
	}
}

func TestStats(t *testing.T) {
	c := New(DefaultConfig[string, string]())
	defer func() { _ = c.Close() }()
	_ = c.Set("x", "y", time.Minute)
	_, _ = c.Get("x")
	_, _ = c.Get("missing")
	c.Delete("x")
	s := c.Stats()
	if s.Hits != 1 || s.Misses != 1 || s.Sets != 1 || s.Deletes != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestTTLExpireAndClear(t *testing.T) {
	clock := NewManualClock(time.Unix(100, 0))
	reasons := make(map[EvictionReason]int)
	c := New(Config[string, string]{DefaultTTL: time.Minute, Clock: clock, OnEvict: func(_ string, _ string, reason EvictionReason) {
		reasons[reason]++
	}})
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Set("default", "a", 0); err != nil {
		t.Fatal(err)
	}
	if ttl, ok := c.TTL("default"); !ok || ttl != time.Minute {
		t.Fatalf("default TTL = %v, %v", ttl, ok)
	}
	if !c.Expire("default", NoExpiration) {
		t.Fatal("Expire(NoExpiration) failed")
	}
	if ttl, ok := c.TTL("default"); !ok || ttl != NoExpiration {
		t.Fatalf("persistent TTL = %v, %v", ttl, ok)
	}
	if !c.Expire("default", 2*time.Second) {
		t.Fatal("Expire failed")
	}
	clock.Advance(2 * time.Second)
	if _, ok := c.TTL("default"); ok {
		t.Fatal("expired key has a TTL")
	}
	if c.Expire("missing", time.Second) {
		t.Fatal("Expire updated a missing key")
	}
	if c.Expire("missing", -2) {
		t.Fatal("Expire accepted an invalid TTL")
	}

	if err := c.Set("x", "x", NoExpiration); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("y", "y", NoExpiration); err != nil {
		t.Fatal(err)
	}
	c.Clear()
	if c.Len() != 0 || reasons[EvictedCleared] != 2 || reasons[EvictedExpired] != 1 {
		t.Fatalf("len=%d reasons=%v", c.Len(), reasons)
	}
}

func TestValidationAndClosedOperations(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config[string, string]
		want error
	}{
		{name: "invalid ttl", cfg: Config[string, string]{DefaultTTL: -2}, want: ErrInvalidTTL},
		{name: "negative cost", cfg: Config[string, string]{DefaultTTL: time.Minute, Cost: func(string, string) int64 { return -1 }}, want: ErrInvalidCost},
		{name: "max item cost", cfg: Config[string, string]{DefaultTTL: time.Minute, MaxItemCost: 1, Cost: func(string, string) int64 { return 2 }}, want: ErrItemTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(tt.cfg)
			defer func() { _ = c.Close() }()
			if err := c.Set("x", "x", 0); !errors.Is(err, tt.want) {
				t.Fatalf("Set error = %v, want %v", err, tt.want)
			}
			if c.Stats().Rejections != 1 {
				t.Fatalf("rejections = %d", c.Stats().Rejections)
			}
		})
	}

	c := New(Config[string, string]{DefaultTTL: time.Minute})
	if err := c.Set("x", "x", 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("x"); ok {
		t.Fatal("Get returned a value after Close")
	}
	if c.Delete("x") || c.Expire("x", time.Second) {
		t.Fatal("mutation succeeded after Close")
	}
	if _, ok := c.TTL("x"); ok {
		t.Fatal("TTL found a key after Close")
	}
	c.Clear()
}

func TestCleanupCallbackMayClose(t *testing.T) {
	closed := make(chan struct{})
	var c *Memory[string, string]
	c = New(Config[string, string]{
		DefaultTTL:      time.Millisecond,
		CleanupInterval: time.Millisecond,
		OnEvict: func(string, string, EvictionReason) {
			_ = c.Close()
			close(closed)
		},
	})
	if err := c.Set("x", "x", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("eviction callback deadlocked in Close")
	}
}

func BenchmarkGetHit(b *testing.B) {
	c := New(Config[int, int]{DefaultTTL: time.Hour})
	defer func() { _ = c.Close() }()
	_ = c.Set(1, 1, time.Hour)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.Get(1)
		}
	})
}

var _ atomic.Uint64
